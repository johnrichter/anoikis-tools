package boundary_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/boundary"
)

func TestValidate_ConformingDeliverableManifestPasses(t *testing.T) {
	raw := []byte(`{"status":"pass","artifact_paths":["report.md"],"facts":["3 files changed"],"next_action":"none"}`)
	m, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if m.Status != "pass" || m.NextAction != "none" || len(m.ArtifactPaths) != 1 {
		t.Fatalf("m = %+v, want decoded manifest", m)
	}
}

// TestValidate_ControlPlaneVerdictIsAManifest checks the design property directly: a
// control-plane verdict (pass/fail, accept/fix) returns as the message because it IS a
// Manifest, carried in Status — not a different shape this package must also accept.
func TestValidate_ControlPlaneVerdictIsAManifest(t *testing.T) {
	raw := []byte(`{"status":"fix","facts":["2 findings raised"],"next_action":"graft a fix node"}`)
	m, err := boundary.Validate(boundary.ClassControlPlane, raw)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if m.Status != "fix" {
		t.Fatalf("m.Status = %q, want the verdict token", m.Status)
	}
}

func TestValidate_MissingRequiredFieldIsNamedNonConforming(t *testing.T) {
	raw := []byte(`{"artifact_paths":["report.md"],"next_action":"none"}`) // no status
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming", err)
	}
	if !strings.Contains(err.Error(), "status") {
		t.Fatalf("err = %v, want it to name the missing field", err)
	}
	if !strings.Contains(err.Error(), "next_action") {
		t.Fatalf("err = %v, want the expected shape stated in the message", err)
	}
}

func TestValidate_UndeclaredExtraFieldIsNamedNonConforming(t *testing.T) {
	raw := []byte(`{"status":"pass","next_action":"none","body":"the whole deliverable, smuggled in"}`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming", err)
	}
}

func TestValidate_TooManyFactsIsNonConforming(t *testing.T) {
	raw := []byte(`{"status":"pass","next_action":"none","facts":["a","b","c","d","e","f"]}`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming", err)
	}
}

func TestValidate_TrailingContentIsNonConforming(t *testing.T) {
	raw := []byte(`{"status":"pass","next_action":"none"}{"status":"pass","next_action":"none"}`)
	_, err := boundary.Validate(boundary.ClassDeliverable, raw)
	if !errors.Is(err, boundary.ErrNonConforming) {
		t.Fatalf("err = %v, want ErrNonConforming", err)
	}
}

// TestValidate_FB10Regression reproduces the observed failure directly: a dispatch whose
// return carries the full deliverable, at the recorded overflow sizes, must be rejected at the
// boundary — never spilled to disk by any path in this package.
func TestValidate_FB10Regression(t *testing.T) {
	for _, kb := range []int{64, 54, 93} {
		body := strings.Repeat("x", kb*1024)
		raw := []byte(`{"status":"pass","next_action":"none","facts":["` + body + `"]}`)
		_, err := boundary.Validate(boundary.ClassDeliverable, raw)
		if !errors.Is(err, boundary.ErrOverLength) {
			t.Fatalf("%dKB return: err = %v, want ErrOverLength", kb, err)
		}
		if !strings.Contains(err.Error(), "byte ceiling") {
			t.Fatalf("%dKB return: err = %v, want it to state the ceiling", kb, err)
		}
	}
}

func TestValidate_OverLengthNamesCeilingAndObservedSize(t *testing.T) {
	raw := []byte(`{"status":"pass","next_action":"` + strings.Repeat("x", 2000) + `"}`)
	_, err := boundary.Validate(boundary.ClassControlPlane, raw)
	if !errors.Is(err, boundary.ErrOverLength) {
		t.Fatalf("err = %v, want ErrOverLength", err)
	}
	ceiling, cErr := boundary.ClassControlPlane.Ceiling()
	if cErr != nil {
		t.Fatalf("Ceiling: %v", cErr)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(ceiling)) {
		t.Fatalf("err = %v, want it to name the declared ceiling %d", err, ceiling)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(len(raw))) {
		t.Fatalf("err = %v, want it to name the observed size %d", err, len(raw))
	}
}

func TestClassCeiling_UnknownClassIsNamedError(t *testing.T) {
	_, err := boundary.ReturnClass("nonsense").Ceiling()
	if !errors.Is(err, boundary.ErrUnknownClass) {
		t.Fatalf("err = %v, want ErrUnknownClass", err)
	}
}

// TestCeilings_DeclaredOncePerClassAndDistinct checks the two return classes are held to
// different, both-declared ceilings — never one restated number reused for both.
func TestCeilings_DeclaredOncePerClassAndDistinct(t *testing.T) {
	cp, err := boundary.ClassControlPlane.Ceiling()
	if err != nil {
		t.Fatalf("Ceiling: %v", err)
	}
	dm, err := boundary.ClassDeliverable.Ceiling()
	if err != nil {
		t.Fatalf("Ceiling: %v", err)
	}
	if cp <= 0 || dm <= 0 || cp == dm {
		t.Fatalf("control-plane ceiling = %d, deliverable ceiling = %d, want two distinct positive ceilings", cp, dm)
	}
}
