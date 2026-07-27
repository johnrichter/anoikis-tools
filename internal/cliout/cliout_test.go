package cliout_test

import (
	"fmt"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/cliout"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/dispatch/routing"
)

var command = []string{cliout.Tool, "step"}

func TestAnUnconfirmedGateReportsAsAPreconditionUnmet(t *testing.T) {
	held := &routing.UnmetError{Subject: "effort", Gates: []routing.Unmet{
		{Gate: "g", Signal: "the release repo exists", Reason: "gate is unconfirmed"},
	}}

	result, err := cliout.Failure(command, "engine", fmt.Errorf("step: %w", held))
	if err != nil {
		t.Fatalf("build the record: %v", err)
	}
	if result.Status.ExitCode() != 30 {
		t.Errorf("an unconfirmed gate exited %d, want 30", result.Status.ExitCode())
	}
	if len(result.Errors) != 1 || result.Errors[0].Code != "precondition_unmet.routing.gate_unconfirmed" {
		t.Fatalf("the record carries %+v, want the routing diagnostic that names the gate", result.Errors)
	}
}

func TestAGateAtADispatchReportsAsAPreconditionUnmet(t *testing.T) {
	refused := routing.NotDispatchable(dag.Detail{
		ID: "g", DeliverableKind: dag.KindGate, Precondition: &dag.Precondition{Signal: "the release repo exists"},
	})

	result, err := cliout.Failure(command, "engine", refused)
	if err != nil {
		t.Fatalf("build the record: %v", err)
	}
	if result.Status.ExitCode() != 30 {
		t.Errorf("a gate at a dispatch exited %d, want 30", result.Status.ExitCode())
	}
}

func TestAnUnrecognisedFailureIsStillInternal(t *testing.T) {
	result, err := cliout.Failure(command, "engine", fmt.Errorf("something broke"))
	if err != nil {
		t.Fatalf("build the record: %v", err)
	}
	if result.Status.ExitCode() == 30 {
		t.Error("an ordinary failure was reported as an unmet precondition")
	}
}
