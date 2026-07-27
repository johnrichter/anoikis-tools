package routing

import (
	"fmt"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/claude-shared-tooling/go/clikit"
)

// maxLine is the longest single-line message a diagnostic may carry, so a
// node's own free-text signal can never invalidate the record reporting it.
const maxLine = 4096

// DepSource reports one node's declared dependencies. The dependency graph
// itself lives outside this package — the blocking check needs only its edges,
// which is exactly what the work graph already exposes.
type DepSource interface {
	Deps(id string) []string
}

// Gates indexes a graph's gates by node id.
type Gates map[string]dag.Precondition

// Unmet is one gate standing in a node's way: which gate it is, what it
// requires, and why that requirement is not yet met.
type Unmet struct {
	Gate   string `json:"gate"`
	Signal string `json:"signal"`
	Reason string `json:"reason"`
}

// Blocking returns the unconfirmed gates between node id and its dispatch:
// every gate it depends on, directly or through other nodes, whose confirmation
// is absent, incomplete, or made against a different signal.
//
// The walk continues past a blocking gate rather than stopping at it, so one
// pass names every gate an operator has to act on. Order follows the declared
// edges, so the same graph always reports the same list.
func Blocking(deps DepSource, gates Gates, id string) []Unmet {
	var out []Unmet
	seen := map[string]bool{id: true}
	queue := append([]string(nil), deps.Deps(id)...)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if seen[next] {
			continue
		}
		seen[next] = true
		queue = append(queue, deps.Deps(next)...)
		precondition, isGate := gates[next]
		if !isGate {
			continue
		}
		if _, err := Confirmed(precondition); err != nil {
			out = append(out, Unmet{Gate: next, Signal: precondition.Signal, Reason: err.Error()})
		}
	}
	return out
}

// Admit refuses node id while any gate it depends on is unconfirmed, and
// returns nil when none is.
//
// This is the guard over a gate's dependents. The guard over the gate itself is
// Route: a gate resolves to no stages and no role, so it is never dispatched in
// the first place.
func Admit(deps DepSource, gates Gates, id string) error {
	unmet := Blocking(deps, gates, id)
	if len(unmet) == 0 {
		return nil
	}
	return &UnmetError{Subject: id, Gates: unmet}
}

// UnmetError reports work held back by one or more unconfirmed gates.
//
// It is a precondition, not a failure: nothing went wrong, the state is exactly
// as it was, and the one act that clears it is an operator's confirmation. That
// is why it reports as clikit's precondition-unmet class and never as an
// internal error, and why no code path here treats reaching the gate as having
// satisfied it.
type UnmetError struct {
	// Subject is what is being held: one node, or the effort whose outstanding
	// work waits on the gates named beside it.
	Subject string
	Gates   []Unmet
}

func (e *UnmetError) Error() string {
	parts := make([]string, 0, len(e.Gates))
	for _, u := range e.Gates {
		parts = append(parts, fmt.Sprintf("%s (%s)", u.Gate, u.Reason))
	}
	return fmt.Sprintf("routing: %s waits on %d unconfirmed gate(s): %s", e.Subject, len(e.Gates), strings.Join(parts, "; "))
}

// Status is the outcome class an unconfirmed gate reports as: precondition
// unmet, whose exit code is 30.
func (e *UnmetError) Status() clikit.Status { return clikit.StatusPreconditionUnmet }

// Diagnostic renders e as the one structured diagnostic a caller emits for it,
// naming every blocking gate and the three facts a confirmation must record.
func (e *UnmetError) Diagnostic() (clikit.Diagnostic, error) {
	names := make([]string, 0, len(e.Gates))
	for _, u := range e.Gates {
		names = append(names, u.Gate)
	}
	return clikit.NewError(
		"precondition_unmet.routing.gate_unconfirmed",
		line(e.Error()),
		clikit.Manual("have an operator confirm each named gate's signal, and record who confirmed, when, and the signal confirmed against; a gate is satisfied by that record and by nothing else"),
		map[string]any{"subject": e.Subject, "gates": names},
	)
}

// NotDispatchableError reports a node that reached a dispatch though what
// verifies it is an operator's confirmation. Like an unmet gate it is a
// precondition rather than a fault, and it never degrades into a dispatch:
// no role can author what a gate does not produce.
type NotDispatchableError struct {
	Node   string
	Signal string
	// Reason says why the gate is not yet satisfied, and is empty when it is —
	// a confirmed gate is settled from its record, still never by a dispatch.
	Reason string
}

// NotDispatchable reports the node detail describes as one no dispatch can
// verify, carrying the signal it waits on and the state of its record.
func NotDispatchable(detail dag.Detail) *NotDispatchableError {
	e := &NotDispatchableError{Node: detail.ID}
	if detail.Precondition == nil {
		e.Reason = ErrNoPrecondition.Error()
		return e
	}
	e.Signal = detail.Precondition.Signal
	if _, err := Confirmed(*detail.Precondition); err != nil {
		e.Reason = err.Error()
	}
	return e
}

func (e *NotDispatchableError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("routing: node %s is confirmed, and is settled from that record rather than by a dispatch", e.Node)
	}
	return fmt.Sprintf("routing: node %s is verified by an operator confirmation of %q, not by a dispatch: %s", e.Node, e.Signal, e.Reason)
}

// Status is the outcome class a gate at a dispatch reports as: precondition
// unmet, whose exit code is 30.
func (e *NotDispatchableError) Status() clikit.Status { return clikit.StatusPreconditionUnmet }

// Diagnostic renders e as the one structured diagnostic a caller emits for it.
func (e *NotDispatchableError) Diagnostic() (clikit.Diagnostic, error) {
	return clikit.NewError(
		"precondition_unmet.routing.gate_not_dispatchable",
		line(e.Error()),
		clikit.Manual("have an operator confirm this gate's signal and record who confirmed, when, and the signal confirmed against, then settle the node from that record; no agent stands in for it"),
		map[string]any{"node": e.Node, "signal": e.Signal},
	)
}

// line collapses s into the single bounded line a diagnostic message requires.
func line(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxLine {
		return s[:maxLine-3] + "..."
	}
	return s
}
