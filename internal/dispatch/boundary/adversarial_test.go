package boundary_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/boundary"
)

// Adversarial tests supplementing the implementer's suite: edge/boundary cases and paths not
// exercised by the existing tests (verified via coverage gap in Validate at the class-lookup
// branch), plus a direct check on the "content never returns as message" design property.

func TestValidate_UnknownClassRejectedBeforeParsing(t *testing.T) {
	// Exercises Validate's own class.Ceiling() error path (distinct from calling
	// ReturnClass.Ceiling() directly, which the existing suite already covers).
	_, err := boundary.Validate(boundary.ReturnClass("bogus"), []byte(`{"status":"pass","next_action":"none"}`))
	if !errors.Is(err, boundary.ErrUnknownClass) {
		t.Fatalf("err = %v, want ErrUnknownClass", err)
	}
}

func TestValidate_EmptyRawIsNonConforming(t *testing.T) {
	_, err := boundary.Validate(boundary.ClassDeliverable, []byte(``))
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming for empty input", err)
	}
}

func TestValidate_GarbageNonJSONIsNonConforming(t *testing.T) {
	_, err := boundary.Validate(boundary.ClassDeliverable, []byte(`not json at all`))
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming for non-JSON input", err)
	}
}

func TestValidate_WhitespaceOnlyStatusIsNonConforming(t *testing.T) {
	raw := []byte(`{"status":"   ","next_action":"none"}`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming for whitespace-only required field", err)
	}
}

func TestValidate_WhitespaceOnlyNextActionIsNonConforming(t *testing.T) {
	raw := []byte(`{"status":"pass","next_action":"\t\n"}`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming for whitespace-only next_action", err)
	}
}

func TestValidate_ExactlyAtCeilingPasses(t *testing.T) {
	ceiling, err := boundary.ClassControlPlane.Ceiling()
	if err != nil {
		t.Fatalf("Ceiling: %v", err)
	}
	prefix := `{"status":"pass","next_action":"`
	suffix := `"}`
	pad := ceiling - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("ceiling %d too small for fixed overhead", ceiling)
	}
	raw := []byte(prefix + strings.Repeat("a", pad) + suffix)
	if len(raw) != ceiling {
		t.Fatalf("constructed raw len = %d, want exactly ceiling %d", len(raw), ceiling)
	}
	if _, err := boundary.Validate(boundary.ClassControlPlane, raw); err != nil {
		t.Fatalf("Validate at exactly the ceiling: %v, want it to pass (ceiling is inclusive)", err)
	}
}

func TestValidate_OneByteOverCeilingIsRejected(t *testing.T) {
	ceiling, err := boundary.ClassControlPlane.Ceiling()
	if err != nil {
		t.Fatalf("Ceiling: %v", err)
	}
	prefix := `{"status":"pass","next_action":"`
	suffix := `"}`
	pad := ceiling - len(prefix) - len(suffix) + 1
	raw := []byte(prefix + strings.Repeat("a", pad) + suffix)
	if len(raw) != ceiling+1 {
		t.Fatalf("constructed raw len = %d, want exactly ceiling+1 = %d", len(raw), ceiling+1)
	}
	_, err = boundary.Validate(boundary.ClassControlPlane, raw)
	if !errors.Is(err, boundary.ErrOverLength) {
		t.Fatalf("err = %v, want ErrOverLength one byte over the ceiling", err)
	}
}

func TestValidate_ExactlyMaxFactsPasses(t *testing.T) {
	raw := []byte(`{"status":"pass","next_action":"none","facts":["a","b","c","d","e"]}`)
	if _, err := boundary.Validate(boundary.ClassDeliverable, raw); err != nil {
		t.Fatalf("Validate with exactly the max fact count: %v, want it to pass", err)
	}
}

// TestValidate_NeverReturnsPartialManifestOnRejection asserts the "no degraded-result sentinel"
// invariant structurally: every rejected call returns the zero Manifest, never a partially
// populated one a caller could mistake for a usable (if degraded) result.
func TestValidate_NeverReturnsPartialManifestOnRejection(t *testing.T) {
	cases := [][]byte{
		[]byte(`{"status":"pass"}`),                                      // missing next_action
		[]byte(`{"status":"pass","next_action":"none","extra":"field"}`), // undeclared field
		[]byte(`not json`),
	}
	for _, raw := range cases {
		m, err := boundary.Validate(boundary.ClassDeliverable, raw)
		if err == nil {
			t.Fatalf("raw %q: want an error", raw)
		}
		if m.Status != "" || m.NextAction != "" || len(m.ArtifactPaths) != 0 || len(m.Facts) != 0 {
			t.Fatalf("raw %q: m = %+v, want the zero Manifest on rejection (no degraded result)", raw, m)
		}
	}
}

// TestValidateBatch_MixedGoodAndBadPreservesPerItemOutcome extends the existing 2-item case to
// a larger, order-scrambled batch: every item's outcome must trace to its own ID regardless of
// where in the batch it sits relative to failures.
func TestValidateBatch_MixedGoodAndBadPreservesPerItemOutcome(t *testing.T) {
	items := []boundary.Item{
		{ID: "a-good", Raw: []byte(`{"status":"pass","next_action":"none"}`)},
		{ID: "b-bad", Raw: []byte(`{"verdict":"fix"}`)},
		{ID: "c-good", Raw: []byte(`{"status":"fail","next_action":"escalate"}`)},
		{ID: "d-bad-overlength", Raw: []byte(`{"status":"pass","next_action":"` + strings.Repeat("x", 5000) + `"}`)},
		{ID: "e-good", Raw: []byte(`{"status":"pass","next_action":"retry"}`)},
	}
	results := boundary.ValidateBatch(boundary.ClassControlPlane, items)
	want := map[string]bool{"a-good": true, "b-bad": false, "c-good": true, "d-bad-overlength": false, "e-good": true}
	for i, r := range results {
		if r.ID != items[i].ID {
			t.Fatalf("results[%d].ID = %q, want %q", i, r.ID, items[i].ID)
		}
		if r.OK() != want[r.ID] {
			t.Fatalf("results[%d] (%s).OK() = %v, want %v", i, r.ID, r.OK(), want[r.ID])
		}
	}
	if !errors.Is(results[3].Err, boundary.ErrOverLength) {
		t.Fatalf("d-bad-overlength err = %v, want ErrOverLength", results[3].Err)
	}
	if !errors.Is(results[1].Err, boundary.ErrNonConforming) {
		t.Fatalf("b-bad err = %v, want ErrNonConforming", results[1].Err)
	}
}

func TestValidateBatch_EmptyBatchProducesNoResults(t *testing.T) {
	results := boundary.ValidateBatch(boundary.ClassControlPlane, nil)
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0 for an empty batch", len(results))
	}
}
