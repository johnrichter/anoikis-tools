package boundary_test

import (
	"errors"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dispatch/boundary"
)

// TestValidateBatch_FB7Regression reproduces FB7 at this package's scope: a four-task batch
// where a malformed schema caused every reviewer emit to be schema-invalid. The fix under test
// is that every rejection surfaces as its own named error — four failures, not four silently
// absorbed retries collapsed into one round.
func TestValidateBatch_FB7Regression(t *testing.T) {
	items := make([]boundary.Item, 4)
	for i := range items {
		items[i] = boundary.Item{ID: "task-" + string(rune('1'+i)), Raw: []byte(`{"verdict":"fix"}`)} // missing status/next_action
	}
	results := boundary.ValidateBatch(boundary.ClassControlPlane, items)
	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4 (one outcome per item, never merged)", len(results))
	}
	rejected := 0
	for i, r := range results {
		if r.ID != items[i].ID {
			t.Fatalf("results[%d].ID = %q, want %q", i, r.ID, items[i].ID)
		}
		if r.OK() {
			t.Fatalf("results[%d] validated, want it rejected", i)
		}
		if !errors.Is(r.Err, boundary.ErrNonConforming) {
			t.Fatalf("results[%d].Err = %v, want ErrNonConforming", i, r.Err)
		}
		rejected++
	}
	if rejected != len(items) {
		t.Fatalf("rejected = %d, want %d — one caller-visible named error per rejection", rejected, len(items))
	}
}

func TestValidateBatch_OneBadItemDoesNotFailTheOthers(t *testing.T) {
	items := []boundary.Item{
		{ID: "good", Raw: []byte(`{"status":"pass","next_action":"none"}`)},
		{ID: "bad", Raw: []byte(`{"verdict":"fix"}`)},
	}
	results := boundary.ValidateBatch(boundary.ClassControlPlane, items)
	if !results[0].OK() {
		t.Fatalf("results[0].Err = %v, want nil — one item's rejection must not affect another's", results[0].Err)
	}
	if results[1].OK() {
		t.Fatal("results[1] validated, want it rejected")
	}
}
