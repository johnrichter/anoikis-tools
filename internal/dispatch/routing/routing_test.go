package routing_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/dispatch/routing"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// harness is a policy with one route per agent-authored kind and one builder
// role behind each, so a gate that leaked onto a build path would land on a
// role that authors artifacts.
func harness() *policy.Harness {
	return &policy.Harness{
		SchemaVersion: policy.SchemaVersion,
		Name:          "test",
		IDScheme:      "opaque",
		Workflow: policy.Workflow{Stages: []policy.Stage{
			{Stage: "build", Role: "builder", Model: "test-model"},
		}},
		Roles: map[string]policy.Role{
			"builder":  {Agent: "software-engineer", Brief: "build it", Builder: true},
			"writer":   {Agent: "doc-writer", Brief: "write it", Builder: true},
			"reviewer": {Agent: "quality-reviewer", Brief: "judge it"},
		},
		Routes: map[string]policy.Route{
			"code": {Stages: []policy.Stage{{Stage: "build", Role: "builder", Model: "test-model"}}, ReviewRole: "reviewer"},
			"docs": {Stages: []policy.Stage{{Stage: "build", Role: "writer", Model: "test-model"}}, ReviewRole: "reviewer"},
		},
		Gates: policy.GateTaxonomy{MainBranch: "main", BuildBranch: "build", Verdicts: []string{"pass", "fix"}, ReviewRole: "reviewer"},
	}
}

func gateNode() dag.Detail {
	return dag.Detail{
		ID:              "g",
		DeliverableKind: dag.KindGate,
		Precondition:    &dag.Precondition{Signal: signal},
	}
}

func TestKindsAreCodeDocsAndGate(t *testing.T) {
	want := []dag.DeliverableKind{dag.KindCode, dag.KindDocs, dag.KindGate}
	if !slices.Equal(routing.Kinds, want) {
		t.Fatalf("routed kinds are %v, want %v", routing.Kinds, want)
	}
}

func TestEveryWorkGraphKindIsRouted(t *testing.T) {
	for _, kind := range dag.AllKinds {
		if !slices.Contains(routing.Kinds, kind) {
			t.Errorf("the work graph declares kind %q, which routing does not decide over", kind)
		}
	}
}

func TestRoutingIsExhaustiveOverEveryKind(t *testing.T) {
	h := harness()
	for _, kind := range routing.Kinds {
		detail := dag.Detail{ID: "n", DeliverableKind: kind}
		if kind == dag.KindGate {
			detail = gateNode()
		}
		resolved, err := routing.Route(h, detail)
		if err != nil {
			t.Fatalf("kind %q: %v", kind, err)
		}
		if resolved.Kind != kind {
			t.Errorf("kind %q resolved as %q", kind, resolved.Kind)
		}
		if !resolved.Verification.Known() {
			t.Errorf("kind %q resolved to verification %q, which is outside the closed set", kind, resolved.Verification)
		}
	}
}

func TestGateResolvesToNoRoleAtAll(t *testing.T) {
	resolved, err := routing.Route(harness(), gateNode())
	if err != nil {
		t.Fatalf("route a gate: %v", err)
	}
	if resolved.Verification != routing.ByOperator {
		t.Errorf("a gate resolved to %q, want %q", resolved.Verification, routing.ByOperator)
	}
	if resolved.Dispatched() {
		t.Error("a gate reported as dispatched; its verification is an operator confirmation")
	}
	if len(resolved.Stages) != 0 {
		t.Errorf("a gate resolved to stages %v; no agent runs a gate", resolved.Stages)
	}
	if resolved.ReviewRole != "" {
		t.Errorf("a gate resolved to review role %q; there is no artifact to review", resolved.ReviewRole)
	}
	if resolved.Signal != signal {
		t.Errorf("a gate resolved to signal %q, want the one it declares", resolved.Signal)
	}
}

func TestAgentKindsResolveToDeclaredRoles(t *testing.T) {
	h := harness()
	for _, kind := range dag.AuthoredKinds {
		resolved, err := routing.Route(h, dag.Detail{ID: "n", DeliverableKind: kind})
		if err != nil {
			t.Fatalf("kind %q: %v", kind, err)
		}
		if !resolved.Dispatched() {
			t.Errorf("kind %q is authored by an agent but did not resolve to a dispatch", kind)
		}
		if len(resolved.Stages) == 0 {
			t.Errorf("kind %q resolved to no stage", kind)
		}
		for _, s := range resolved.Stages {
			if _, ok := h.Roles[s.Role]; !ok {
				t.Errorf("kind %q stage %q names undeclared role %q", kind, s.Stage, s.Role)
			}
		}
	}
}

func TestAnUnroutedKindIsRefusedRatherThanBuilt(t *testing.T) {
	_, err := routing.Route(harness(), dag.Detail{ID: "n", DeliverableKind: "invented"})
	if !errors.Is(err, routing.ErrUnroutedKind) {
		t.Fatalf("an unrouted kind returned %v, want ErrUnroutedKind", err)
	}
}

func TestAPolicyThatRoutesAGateIsRefused(t *testing.T) {
	h := harness()
	h.Routes[string(dag.KindGate)] = policy.Route{
		Stages: []policy.Stage{{Stage: "build", Role: "builder", Model: "test-model"}},
	}
	if err := h.Validate(); err == nil {
		t.Error("a harness declaring gate stages validated")
	}
	_, err := routing.Route(h, gateNode())
	if !errors.Is(err, routing.ErrDispatchRouteDeclared) {
		t.Fatalf("a policy declaring gate stages returned %v, want ErrDispatchRouteDeclared", err)
	}
}

func TestAGateWithNoPreconditionIsRefused(t *testing.T) {
	_, err := routing.Route(harness(), dag.Detail{ID: "g", DeliverableKind: dag.KindGate})
	if !errors.Is(err, routing.ErrNoPrecondition) {
		t.Fatalf("a gate with no precondition returned %v, want ErrNoPrecondition", err)
	}
}

func TestNoHarnessIsRefusedRatherThanAssumed(t *testing.T) {
	if _, err := routing.Route(nil, gateNode()); err == nil {
		t.Fatal("routing with no harness policy returned a path")
	}
}

func TestVerificationOfRefusesAnUnknownKind(t *testing.T) {
	if _, ok := routing.VerificationOf("invented"); ok {
		t.Fatal("an unknown kind reported a verification instead of being refused")
	}
}
