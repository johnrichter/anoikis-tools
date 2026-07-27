package routing_test

import (
	"errors"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/dispatch/routing"
)

const signal = "the release repo exists and is clonable"

func complete() dag.Precondition {
	return dag.Precondition{
		Signal: signal,
		Confirmation: &dag.Confirmation{
			By:      "operator",
			At:      "2026-07-27T09:15:00Z",
			Against: signal,
		},
	}
}

func TestACompleteRecordConfirmsTheGate(t *testing.T) {
	c, err := routing.Confirmed(complete())
	if err != nil {
		t.Fatalf("a complete record was refused: %v", err)
	}
	if c.By == "" || c.At == "" || c.Against != signal {
		t.Errorf("the confirmed record lost a fact: %+v", c)
	}
}

func TestAGateWithNoRecordIsUnconfirmed(t *testing.T) {
	if _, err := routing.Confirmed(dag.Precondition{Signal: signal}); !errors.Is(err, routing.ErrUnconfirmed) {
		t.Fatalf("a gate with no record returned %v, want ErrUnconfirmed", err)
	}
}

func TestAnIncompleteRecordIsRefused(t *testing.T) {
	cases := map[string]func(*dag.Confirmation){
		"no operator":      func(c *dag.Confirmation) { c.By = "" },
		"no time":          func(c *dag.Confirmation) { c.At = "" },
		"no signal":        func(c *dag.Confirmation) { c.Against = "" },
		"unparseable time": func(c *dag.Confirmation) { c.At = "last tuesday" },
		"whitespace only":  func(c *dag.Confirmation) { c.By = "   " },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			p := complete()
			breakIt(p.Confirmation)
			if _, err := routing.Confirmed(p); !errors.Is(err, routing.ErrIncompleteRecord) {
				t.Fatalf("%s returned %v, want ErrIncompleteRecord", name, err)
			}
		})
	}
}

func TestARecordAboutAnotherSignalDoesNotConfirmThisGate(t *testing.T) {
	p := complete()
	p.Confirmation.Against = "some other precondition entirely"
	if _, err := routing.Confirmed(p); !errors.Is(err, routing.ErrSignalMismatch) {
		t.Fatalf("a mismatched record returned %v, want ErrSignalMismatch", err)
	}
}

func TestAPreconditionWithNoSignalIsRefused(t *testing.T) {
	p := complete()
	p.Signal = ""
	if _, err := routing.Confirmed(p); !errors.Is(err, routing.ErrNoSignal) {
		t.Fatalf("a precondition with no signal returned %v, want ErrNoSignal", err)
	}
}
