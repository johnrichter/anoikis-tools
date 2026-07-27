package boundary_test

import (
	"errors"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/boundary"
)

func TestValidate_TopLevelArrayIsNonConforming(t *testing.T) {
	raw := []byte(`[{"status":"pass","next_action":"none"}]`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming for a top-level array", err)
	}
}

func TestValidate_NullRequiredFieldIsNonConforming(t *testing.T) {
	raw := []byte(`{"status":null,"next_action":"none"}`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming for a null required field", err)
	}
}

func TestValidate_WrongTypeForFieldIsNonConforming(t *testing.T) {
	raw := []byte(`{"status":123,"next_action":"none"}`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming for wrong-typed field", err)
	}
}

func TestValidate_DuplicateKeysLastOneDoesNotSmuggleContent(t *testing.T) {
	// A conforming-looking manifest that repeats "status": Go's decoder takes the last value,
	// so this must still validate as an ordinary manifest, not be treated as an extra channel.
	raw := []byte(`{"status":"pending","status":"pass","next_action":"none"}`)
	m, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if m.Status != "pass" {
		t.Fatalf("m.Status = %q, want last-value-wins %q", m.Status, "pass")
	}
}

func TestValidateBatch_DuplicateIDsStillProduceOnePerItemResult(t *testing.T) {
	// Batch validation is positional, not keyed by ID: duplicate IDs must not cause results to
	// merge or collapse — every item still gets its own outcome.
	items := []boundary.Item{
		{ID: "same", Raw: []byte(`{"status":"pass","next_action":"none"}`)},
		{ID: "same", Raw: []byte(`{"verdict":"fix"}`)},
	}
	results := boundary.ValidateBatch(boundary.ClassControlPlane, items)
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2 even with duplicate IDs", len(results))
	}
	if !results[0].OK() {
		t.Fatalf("results[0].Err = %v, want nil", results[0].Err)
	}
	if results[1].OK() {
		t.Fatal("results[1] validated, want it rejected")
	}
}

func TestValidate_EmptyFactsSliceIsDistinctFromAbsent(t *testing.T) {
	raw := []byte(`{"status":"pass","next_action":"none","facts":[]}`)
	m, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(m.Facts) != 0 {
		t.Fatalf("m.Facts = %v, want empty", m.Facts)
	}
}
