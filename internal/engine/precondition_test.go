package engine_test

import (
	"errors"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/dispatch/routing"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/ids"
)

const gateSignal = "the release repo exists and is clonable"

// operatorGate is a node an operator confirms. It claims no resource surface
// because it authors nothing to claim one for.
func operatorGate(id string, status dag.Status, deps ...string) dag.Node {
	return dag.Node{
		ID: id, Title: id, Status: status, Deps: deps,
		VerifyTier: dag.VerifyCheap, DetailRef: "nodes/" + id + ".json", MaxAttempts: 1,
	}
}

func gateDetail(id string, record *dag.Confirmation) dag.Detail {
	return dag.Detail{
		SchemaVersion:   dag.SchemaVersion,
		ID:              id,
		Intent:          "confirm the signal before anything downstream is built",
		DeliverableKind: dag.KindGate,
		Acceptance:      []string{"an operator confirmed the signal, and the record says who, when and against what"},
		Precondition:    &dag.Precondition{Signal: gateSignal, Confirmation: record},
	}
}

func confirmation() *dag.Confirmation {
	return &dag.Confirmation{By: "operator", At: "2026-07-27T09:15:00Z", Against: gateSignal}
}

// gated is an effort where one code node waits on one operator gate.
func gated(gateStatus dag.Status, record *dag.Confirmation) (dag.State, map[string]dag.Detail) {
	st := state([]dag.Node{
		operatorGate("g", gateStatus),
		node("a", "svc/a", dag.StatusBlocked, "g"),
	}, dag.GatePending)
	details := map[string]dag.Detail{
		"g": gateDetail("g", record),
		"a": {SchemaVersion: dag.SchemaVersion, ID: "a", Intent: "build it", DeliverableKind: dag.KindCode, Acceptance: []string{"built"}},
	}
	return st, details
}

func TestValidateAcceptsAGateNode(t *testing.T) {
	st, details := gated(dag.StatusBlocked, nil)
	rep, err := engine.Validate(st, harness(t), ids.Opaque{}, details)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("a gate node made the plan invalid: %+v", rep.Problems)
	}
}

func TestValidateRefusesAGateThatDeclaresADeliverable(t *testing.T) {
	st, details := gated(dag.StatusBlocked, nil)
	broken := details["g"]
	broken.Result = &dag.NodeResult{ArtifactRefs: []string{"docs/out.md"}}
	details["g"] = broken

	rep, err := engine.Validate(st, harness(t), ids.Opaque{}, details)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if rep.OK() {
		t.Fatal("a gate declaring a produced artifact was accepted")
	}
	if rep.Problems[0].Code != "gate.deliverable" {
		t.Errorf("problem code = %q, want gate.deliverable", rep.Problems[0].Code)
	}
}

func TestValidateRefusesAnIncompleteConfirmation(t *testing.T) {
	record := confirmation()
	record.By = ""
	st, details := gated(dag.StatusBlocked, record)

	rep, err := engine.Validate(st, harness(t), ids.Opaque{}, details)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if rep.OK() {
		t.Fatal("a confirmation missing who confirmed was accepted")
	}
	if rep.Problems[0].Code != "gate.confirmation" {
		t.Errorf("problem code = %q, want gate.confirmation", rep.Problems[0].Code)
	}
}

func TestPlanDispatchRefusesAGateAsAPrecondition(t *testing.T) {
	st, details := gated(dag.StatusReady, confirmation())
	_, err := engine.PlanDispatch(st, harness(t), details, []string{"g"}, 0, env)
	var refused *routing.NotDispatchableError
	if !errors.As(err, &refused) {
		t.Fatalf("a gate at a dispatch returned %v, want a NotDispatchableError", err)
	}
	if refused.Status().ExitCode() != 30 {
		t.Errorf("a gate at a dispatch exits %d, want 30", refused.Status().ExitCode())
	}
}

func TestPlanDispatchRefusesANodeBehindAnUnconfirmedGate(t *testing.T) {
	st, details := gated(dag.StatusDone, nil)
	_, err := engine.PlanDispatch(st, harness(t), details, []string{"a"}, 0, env)
	var unmet *routing.UnmetError
	if !errors.As(err, &unmet) {
		t.Fatalf("a node behind an unconfirmed gate returned %v, want an UnmetError", err)
	}
	if unmet.Status().ExitCode() != 30 {
		t.Errorf("an unmet precondition exits %d, want 30", unmet.Status().ExitCode())
	}
}

func TestAnUnconfirmedGateHoldsTheEffort(t *testing.T) {
	st, details := gated(dag.StatusBlocked, nil)
	err := engine.OperatorHold(st, details)
	var unmet *routing.UnmetError
	if !errors.As(err, &unmet) {
		t.Fatalf("an unconfirmed gate returned %v, want an UnmetError", err)
	}
	if unmet.Status().ExitCode() != 30 {
		t.Errorf("an unmet precondition exits %d, want 30", unmet.Status().ExitCode())
	}
	if len(unmet.Gates) != 1 || unmet.Gates[0].Gate != "g" {
		t.Fatalf("the hold names %+v, want the one unconfirmed gate", unmet.Gates)
	}
}

func TestAConfirmedGateHoldsNothing(t *testing.T) {
	st, details := gated(dag.StatusDone, confirmation())
	if err := engine.OperatorHold(st, details); err != nil {
		t.Fatalf("a confirmed gate held the effort: %v", err)
	}
}

func TestMarkingAGateDoneDoesNotSatisfyIt(t *testing.T) {
	st, details := gated(dag.StatusDone, nil)
	if err := engine.OperatorHold(st, details); err == nil {
		t.Fatal("a gate marked done with no confirmation record read as satisfied")
	}
}
