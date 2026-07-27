// Package routing decides how a work node's deliverable is verified, and is
// the only place that decision is made.
//
// A node's deliverable kind fixes its verification. code and docs name an
// artifact an agent authors, so they resolve to the stages the harness policy
// routes them through. A gate names no artifact: it is an operator-precondition
// boundary whose verification is a recorded operator confirmation, so it
// resolves to no stages and no role at all. Nothing about an operator-confirmed
// kind reads the policy's routes or its default workflow, so there is no code
// path by which a gate acquires a builder or a writer — a role that authors
// artifacts, handed a gate, would produce one instead of verifying a signal.
//
// Routing is exhaustive over the kinds it declares and refuses one it does not
// know. The fallback that would quietly send an unrouted kind down the build
// path does not exist.
//
// An unconfirmed gate is satisfied by nothing but a complete confirmation
// record — who confirmed, when, and the signal confirmed against. Until then it
// blocks every node downstream of it and reports as precondition-unmet:
// nothing has gone wrong, the state is exactly as it was, and the one act that
// clears it is an operator's.
package routing

import (
	"fmt"
	"maps"
	"slices"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// Verification is how a node's deliverable is shown to have been delivered.
type Verification string

// The closed verification set. Every deliverable kind maps onto exactly one of
// these, and a caller branches over both.
const (
	// ByAgent is verified by dispatching the node's stages to agents, which
	// author the artifact it names and check it.
	ByAgent Verification = "agent_dispatch"
	// ByOperator is verified by a recorded operator confirmation. No agent runs
	// and no artifact is produced.
	ByOperator Verification = "operator_confirmation"
)

// Known reports whether v is one of the two canonical verifications.
func (v Verification) Known() bool { return v == ByAgent || v == ByOperator }

// verification is the whole routing table: every deliverable kind, and how it
// is verified. It is also the enumeration — Kinds is derived from it — so a
// kind cannot be introduced without deciding how it is verified, and a kind
// absent from it is refused rather than defaulted onto a build path.
var verification = map[dag.DeliverableKind]Verification{
	dag.KindCode: ByAgent,
	dag.KindDocs: ByAgent,
	dag.KindGate: ByOperator,
}

// Kinds is every deliverable kind routing decides over, sorted so a message
// listing them reads the same everywhere.
var Kinds = slices.Sorted(maps.Keys(verification))

// Named refusals. Each pairs with a zero Resolved — a refused kind is never
// traded for a route, least of all the build route.
var (
	// ErrUnroutedKind reports a deliverable kind outside the routed set. It is
	// the refusal that stands where a default-to-builder fallback would be.
	ErrUnroutedKind = fmt.Errorf("routing: deliverable kind is outside the routed set")

	// ErrDispatchRouteDeclared reports a harness policy that declares stages
	// for an operator-confirmed kind. Such a route exists only to dispatch a
	// gate, so it is refused when the policy is read rather than honoured.
	ErrDispatchRouteDeclared = fmt.Errorf("routing: an operator-confirmed kind must not declare a dispatch route")

	// ErrNoPrecondition reports a gate that declares no precondition, so there
	// is no signal for an operator to confirm and nothing that could ever
	// satisfy it.
	ErrNoPrecondition = fmt.Errorf("routing: a gate declares no precondition")

	// ErrNoStages reports an agent-verified kind whose route and whose harness
	// workflow both resolve to nothing to run.
	ErrNoStages = fmt.Errorf("routing: an agent-verified kind resolves to no stage")

	// ErrUndeclaredRole reports a resolved stage naming a role the harness
	// never declared, so nothing could be dispatched for it.
	ErrUndeclaredRole = fmt.Errorf("routing: a resolved stage names a role the harness does not declare")
)

// VerificationOf reports how kind is verified. ok is false for a kind outside
// the table; the caller refuses it rather than choosing a route for it.
func VerificationOf(kind dag.DeliverableKind) (v Verification, ok bool) {
	v, ok = verification[kind]
	return v, ok
}

// Resolved is one node's verification path.
type Resolved struct {
	Kind         dag.DeliverableKind `json:"kind"`
	Verification Verification        `json:"verification"`
	// Stages are the agent stages the node runs, taken from the route its kind
	// selects. Empty for an operator-confirmed kind, which no agent runs.
	Stages []policy.Stage `json:"stages,omitempty"`
	// ReviewRole is the role that reviews the node's artifact, empty when there
	// is no artifact to review.
	ReviewRole string `json:"review_role,omitempty"`
	// Signal is the operator signal a gate requires, carried here so a caller
	// reporting an unmet precondition names what is still missing.
	Signal string `json:"signal,omitempty"`
}

// Dispatched reports whether this path hands the node to an agent.
func (r Resolved) Dispatched() bool { return r.Verification == ByAgent }

// Route resolves how the node that detail describes is verified under harness h.
//
// The kind is looked up first and refused outright if the table does not hold
// it; the switch that follows is over the closed verification set, and its
// default refuses too. Every branch either names a decided path or returns an
// error, so no kind reaches an agent by omission.
func Route(h *policy.Harness, detail dag.Detail) (Resolved, error) {
	if h == nil {
		return Resolved{}, fmt.Errorf("routing: no harness policy supplied; every path is declared and none is assumed")
	}
	v, ok := VerificationOf(detail.DeliverableKind)
	if !ok {
		return Resolved{}, fmt.Errorf("%w: node %s declares %q, and the routed set is %v", ErrUnroutedKind, detail.ID, detail.DeliverableKind, Kinds)
	}
	switch v {
	case ByOperator:
		return operatorRoute(h, detail)
	case ByAgent:
		return agentRoute(h, detail)
	default:
		return Resolved{}, fmt.Errorf("routing: node %s resolves to verification %q, which this package does not implement", detail.ID, v)
	}
}

// operatorRoute resolves a gate. It reads the harness only to refuse a policy
// that tried to give an operator-confirmed kind stages — the owned
// harness-policy contract already refuses one on disk, and this catches a
// policy assembled in memory.
func operatorRoute(h *policy.Harness, detail dag.Detail) (Resolved, error) {
	if _, declared := h.Routes[string(detail.DeliverableKind)]; declared {
		return Resolved{}, fmt.Errorf("%w: routes.%s declares stages for a kind an operator confirms", ErrDispatchRouteDeclared, detail.DeliverableKind)
	}
	if detail.Precondition == nil {
		return Resolved{}, fmt.Errorf("%w: node %s names no signal to confirm", ErrNoPrecondition, detail.ID)
	}
	return Resolved{
		Kind:         detail.DeliverableKind,
		Verification: ByOperator,
		Signal:       detail.Precondition.Signal,
	}, nil
}

// agentRoute resolves a kind an agent authors, through the route its harness
// declares for it.
func agentRoute(h *policy.Harness, detail dag.Detail) (Resolved, error) {
	stages, err := h.StagesFor(detail.DeliverableKind)
	if err != nil {
		return Resolved{}, err
	}
	if len(stages) == 0 {
		return Resolved{}, fmt.Errorf("%w: kind %q", ErrNoStages, detail.DeliverableKind)
	}
	for _, s := range stages {
		if _, ok := h.Roles[s.Role]; !ok {
			return Resolved{}, fmt.Errorf("%w: kind %q stage %q names role %q", ErrUndeclaredRole, detail.DeliverableKind, s.Stage, s.Role)
		}
	}
	return Resolved{
		Kind:         detail.DeliverableKind,
		Verification: ByAgent,
		Stages:       stages,
		ReviewRole:   h.ReviewRoleFor(detail.DeliverableKind),
	}, nil
}

// Collect indexes the gates among a set of node details by node id, the form
// the blocking check consumes. A gate carrying no precondition at all is still
// indexed: it is unconfirmed, which is the reading that blocks, rather than one
// that drops out of the index and reads as satisfied.
func Collect(details map[string]dag.Detail) Gates {
	gates := Gates{}
	for id, d := range details {
		if !d.DeliverableKind.OperatorConfirmed() {
			continue
		}
		if d.Precondition == nil {
			gates[id] = dag.Precondition{}
			continue
		}
		gates[id] = *d.Precondition
	}
	return gates
}
