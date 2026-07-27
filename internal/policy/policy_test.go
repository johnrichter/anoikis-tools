package policy_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// examplePolicy is the policy shipped with the repo, which every test here
// starts from so a change that breaks the documented example is caught.
const examplePolicy = "../../examples/harness-policy.json"

func TestLoadExamplePolicy(t *testing.T) {
	h, err := policy.Load(examplePolicy)
	if err != nil {
		t.Fatalf("load example policy: %v", err)
	}
	for _, kind := range dag.AllKinds {
		if _, err := h.StagesFor(kind); err != nil {
			t.Errorf("StagesFor(%s): %v", kind, err)
		}
	}
	if _, err := h.Prover(); err != nil {
		t.Errorf("Prover: %v", err)
	}
	if _, err := h.Scheme(); err != nil {
		t.Errorf("Scheme: %v", err)
	}
	if !h.TargetsMain("main") || h.TargetsMain("build") {
		t.Errorf("main-branch identification is wrong: main=%v build=%v", h.TargetsMain("main"), h.TargetsMain("build"))
	}
}

// mutate loads the example policy, applies fn to its decoded form, writes it
// to a temp file and loads it again, returning whatever error that produces.
func mutate(t *testing.T, fn func(doc map[string]any)) error {
	t.Helper()
	raw, err := os.ReadFile(examplePolicy)
	if err != nil {
		t.Fatalf("read example policy: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse example policy: %v", err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode mutated policy: %v", err)
	}
	path := filepath.Join(t.TempDir(), "harness-policy.json")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write mutated policy: %v", err)
	}
	_, err = policy.Load(path)
	return err
}

func TestRoutingMustBeExhaustive(t *testing.T) {
	err := mutate(t, func(doc map[string]any) {
		delete(doc["routes"].(map[string]any), string(dag.KindDocs))
	})
	if err == nil {
		t.Fatal("a policy with no route for a declared deliverable kind was accepted")
	}
}

func TestReviewRoleMustNotBeABuilder(t *testing.T) {
	err := mutate(t, func(doc map[string]any) {
		doc["gates"].(map[string]any)["review_role"] = "builder"
	})
	if err == nil {
		t.Fatal("a builder role was accepted as a review role")
	}
}

func TestBackstopCommandIsRequired(t *testing.T) {
	err := mutate(t, func(doc map[string]any) {
		doc["backstop"] = map[string]any{"command": []any{}}
	})
	if err == nil {
		t.Fatal("a policy with no backstop command was accepted; the post-merge check is always on")
	}
}

func TestUnknownIDSchemeIsRefused(t *testing.T) {
	err := mutate(t, func(doc map[string]any) {
		doc["id_scheme"] = "no-such-scheme"
	})
	if err == nil {
		t.Fatal("an unregistered id scheme was accepted")
	}
}

func TestFixVerdictMustBeDeclared(t *testing.T) {
	err := mutate(t, func(doc map[string]any) {
		doc["gates"].(map[string]any)["fix_verdict"] = "rework"
	})
	if err == nil {
		t.Fatal("a fix verdict outside the declared vocabulary was accepted")
	}
}
