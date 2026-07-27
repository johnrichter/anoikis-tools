package routing_test

import (
	"errors"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/dispatch/routing"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// The work graph satisfies the dependency seam with no adapter, which is the
// point of keeping the seam to declared edges alone.
var _ routing.DepSource = (*graph.Graph[string, dag.Node])(nil)

// edges is a dependency graph as declared edges, the only thing the blocking
// check reads.
type edges map[string][]string

func (e edges) Deps(id string) []string { return e[id] }

func TestAnUnconfirmedGateBlocksItsDependents(t *testing.T) {
	deps := edges{"builder": {"g"}}
	gates := routing.Gates{"g": {Signal: signal}}

	err := routing.Admit(deps, gates, "builder")
	var unmet *routing.UnmetError
	if !errors.As(err, &unmet) {
		t.Fatalf("an unconfirmed gate returned %v, want an UnmetError", err)
	}
	if unmet.Status().ExitCode() != 30 {
		t.Errorf("an unmet precondition exits %d, want 30", unmet.Status().ExitCode())
	}
	if len(unmet.Gates) != 1 || unmet.Gates[0].Gate != "g" {
		t.Fatalf("the refusal names %+v, want the one blocking gate", unmet.Gates)
	}
	if unmet.Gates[0].Reason == "" {
		t.Error("the refusal does not say why the gate is unconfirmed")
	}
}

func TestAConfirmedGateAdmitsItsDependents(t *testing.T) {
	deps := edges{"builder": {"g"}}
	gates := routing.Gates{"g": complete()}
	if err := routing.Admit(deps, gates, "builder"); err != nil {
		t.Fatalf("a confirmed gate still blocked its dependent: %v", err)
	}
}

func TestAnIncompleteRecordStillBlocks(t *testing.T) {
	p := complete()
	p.Confirmation.Against = ""
	if err := routing.Admit(edges{"builder": {"g"}}, routing.Gates{"g": p}, "builder"); err == nil {
		t.Fatal("an incomplete record satisfied the gate")
	}
}

func TestAGateBlocksTransitivelyAndNamesEveryOne(t *testing.T) {
	deps := edges{
		"last":   {"middle"},
		"middle": {"first", "g2"},
		"first":  {"g1"},
	}
	gates := routing.Gates{"g1": {Signal: "first signal"}, "g2": {Signal: "second signal"}}

	unmet := routing.Blocking(deps, gates, "last")
	if len(unmet) != 2 {
		t.Fatalf("blocking reported %d gate(s), want both", len(unmet))
	}
	again := routing.Blocking(deps, gates, "last")
	for i := range unmet {
		if again[i] != unmet[i] {
			t.Fatalf("two walks over one graph reported different orders: %+v then %+v", unmet, again)
		}
	}
}

func TestANodeBehindNoGateIsAdmitted(t *testing.T) {
	if err := routing.Admit(edges{"builder": {"other"}}, routing.Gates{"g": {Signal: signal}}, "builder"); err != nil {
		t.Fatalf("a node depending on no gate was blocked: %v", err)
	}
}

func TestTheRefusalRendersAsAnEmittableDiagnostic(t *testing.T) {
	err := routing.Admit(edges{"builder": {"g"}}, routing.Gates{"g": {Signal: "a signal\nspanning lines"}}, "builder")
	var unmet *routing.UnmetError
	if !errors.As(err, &unmet) {
		t.Fatalf("expected an UnmetError, got %v", err)
	}
	diag, buildErr := unmet.Diagnostic()
	if buildErr != nil {
		t.Fatalf("the refusal could not be rendered as a diagnostic: %v", buildErr)
	}
	if diag.Code != "precondition_unmet.routing.gate_unconfirmed" {
		t.Errorf("diagnostic code is %q", diag.Code)
	}
	if diag.Triage.Kind != "manual" {
		t.Errorf("triage kind is %q, want manual: only an operator clears a gate", diag.Triage.Kind)
	}
}

func TestACycleInTheEdgesTerminates(t *testing.T) {
	deps := edges{"a": {"b"}, "b": {"a", "g"}}
	if len(routing.Blocking(deps, routing.Gates{"g": {Signal: signal}}, "a")) != 1 {
		t.Fatal("a cyclic edge set did not report the one gate behind it")
	}
}

func TestCollectIndexesEveryGateIncludingAPreconditionlessOne(t *testing.T) {
	p := complete()
	gates := routing.Collect(map[string]dag.Detail{
		"code": {ID: "code", DeliverableKind: dag.KindCode},
		"g1":   {ID: "g1", DeliverableKind: dag.KindGate, Precondition: &p},
		"g2":   {ID: "g2", DeliverableKind: dag.KindGate},
	})
	if len(gates) != 2 {
		t.Fatalf("collected %d gate(s), want the two gate nodes", len(gates))
	}
	if _, err := routing.Confirmed(gates["g2"]); err == nil {
		t.Error("a gate with no precondition read as satisfied")
	}
}

func TestAGateAtADispatchIsRefusedAsAPrecondition(t *testing.T) {
	err := routing.NotDispatchable(dag.Detail{
		ID: "g", DeliverableKind: dag.KindGate, Precondition: &dag.Precondition{Signal: signal},
	})
	if err.Status().ExitCode() != 30 {
		t.Errorf("a gate at a dispatch exits %d, want 30", err.Status().ExitCode())
	}
	diag, buildErr := err.Diagnostic()
	if buildErr != nil {
		t.Fatalf("the refusal could not be rendered as a diagnostic: %v", buildErr)
	}
	if diag.Code != "precondition_unmet.routing.gate_not_dispatchable" {
		t.Errorf("diagnostic code is %q", diag.Code)
	}
}
