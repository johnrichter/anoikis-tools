package routing

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnrichter/anoikis-tools/internal/dag"
)

// Named refusals of a confirmation. Each pairs with a zero Confirmation: a
// record that earned one of these is not a weaker confirmation, it is none at
// all, and the gate it was offered for stays unmet.
var (
	// ErrNoSignal reports a precondition that states nothing to confirm. An
	// operator cannot confirm a signal that was never declared.
	ErrNoSignal = fmt.Errorf("routing: precondition declares no signal")

	// ErrUnconfirmed reports a gate with no confirmation record at all. This is
	// the ordinary state of a gate before an operator reaches it, and it is
	// reported rather than assumed away.
	ErrUnconfirmed = fmt.Errorf("routing: gate is unconfirmed")

	// ErrIncompleteRecord reports a confirmation record missing one of the three
	// facts that make it accountable — who confirmed, when, and against what.
	ErrIncompleteRecord = fmt.Errorf("routing: confirmation record is incomplete")

	// ErrSignalMismatch reports a record confirming something other than the
	// signal the gate declares. It attests to a different act, so it cannot
	// stand in for this one.
	ErrSignalMismatch = fmt.Errorf("routing: confirmation was made against a different signal")
)

// Confirmed reports p's satisfied confirmation, or why it is not one.
//
// All three facts must be present, the instant must parse, and the signal
// confirmed against must be the signal the gate declares. Anything less is an
// error rather than a partial credit: a gate that could be satisfied by an
// incomplete record, or by a record about some other signal, would be
// auto-satisfied in every way that matters.
func Confirmed(p dag.Precondition) (dag.Confirmation, error) {
	if strings.TrimSpace(p.Signal) == "" {
		return dag.Confirmation{}, ErrNoSignal
	}
	if p.Confirmation == nil {
		return dag.Confirmation{}, fmt.Errorf("%w: %s", ErrUnconfirmed, p.Signal)
	}
	c := *p.Confirmation

	var missing []string
	if strings.TrimSpace(c.By) == "" {
		missing = append(missing, "who confirmed")
	}
	if strings.TrimSpace(c.At) == "" {
		missing = append(missing, "when they confirmed")
	}
	if strings.TrimSpace(c.Against) == "" {
		missing = append(missing, "the signal confirmed against")
	}
	if len(missing) > 0 {
		return dag.Confirmation{}, fmt.Errorf("%w: it does not record %s", ErrIncompleteRecord, strings.Join(missing, " or "))
	}
	if _, err := time.Parse(time.RFC3339, c.At); err != nil {
		return dag.Confirmation{}, fmt.Errorf("%w: %q is not an RFC 3339 instant, so when it happened is unknown", ErrIncompleteRecord, c.At)
	}
	if c.Against != p.Signal {
		return dag.Confirmation{}, fmt.Errorf("%w: recorded against %q, and this gate requires %q", ErrSignalMismatch, c.Against, p.Signal)
	}
	return c, nil
}
