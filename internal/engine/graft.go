package engine

import (
	"fmt"
	"slices"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// Graft is a node the engine inserts into the graph itself, together with the
// log entry that records the insertion.
type Graft struct {
	GateID string       `json:"gate_id"`
	Node   dag.Node     `json:"node"`
	Detail dag.Detail   `json:"detail"`
	Event  dag.LogEvent `json:"event"`
}

// PlanGraft builds the fix node a review's fix verdict calls for.
//
// This is the one build-time mutation of a graph that is otherwise decided
// entirely at plan time, and it stays legitimate because it is mechanical:
// the new node depends on exactly the nodes that were reviewed, claims
// exactly the union of their surfaces, carries the review's own findings
// artifact as its input, and is journalled. No judgement is exercised — the
// review already returned the verdict, and this only acts on it.
//
// The reviewed nodes must agree on a deliverable kind and a gate: a fix that
// spans two kinds or two gates is not one node's worth of work, and inventing
// a routing or a membership for it would be a judgement this refuses to make.
func PlanGraft(st dag.State, h *policy.Harness, scheme ids.Scheme, details map[string]dag.Detail, reviewed []string, findingsRef, at string) (Graft, error) {
	if len(reviewed) == 0 {
		return Graft{}, fmt.Errorf("engine: a fix node needs at least one reviewed node")
	}
	parents := slices.Clone(reviewed)
	slices.Sort(parents)

	gateID, err := singleGate(st, parents)
	if err != nil {
		return Graft{}, err
	}
	kind, err := singleKind(details, parents)
	if err != nil {
		return Graft{}, err
	}
	stages, err := h.StagesFor(kind)
	if err != nil {
		return Graft{}, err
	}

	id, err := scheme.Derive(parents, "fix", st.GraftOrdinal(parents))
	if err != nil {
		return Graft{}, err
	}
	if _, exists := st.Node(id); exists {
		return Graft{}, fmt.Errorf("engine: derived fix-node id %s already exists", id)
	}

	node := dag.Node{
		ID:          id,
		Title:       fmt.Sprintf("Fix findings from %s", joinIDs(parents)),
		Status:      dag.StatusReady,
		Deps:        parents,
		Surface:     unionSurface(st, parents),
		VerifyTier:  dag.VerifyImmediateDeep,
		MaxAttempts: 1,
		GraftedFrom: parents,
	}
	detail := dag.Detail{
		ID:              id,
		Intent:          fmt.Sprintf("Resolve every finding the review of %s raised, as recorded in %s.", joinIDs(parents), findingsRef),
		DeliverableKind: kind,
		Acceptance: []string{
			fmt.Sprintf("Every finding recorded in %s is resolved or explicitly refuted with evidence.", findingsRef),
			"The reviewed nodes' own acceptance criteria still hold.",
		},
		Inputs: &dag.Inputs{SpecRefs: []string{findingsRef}},
	}
	for _, s := range stages {
		detail.Stages = append(detail.Stages, dag.Stage{
			Stage:         s.Stage,
			Role:          s.Role,
			Model:         s.Model,
			ContextWindow: s.ContextWindow,
			Effort:        s.Effort,
		})
	}
	if len(detail.Stages) == 0 {
		return Graft{}, fmt.Errorf("engine: route for %s resolved to no stages", kind)
	}

	return Graft{
		GateID: gateID,
		Node:   node,
		Detail: detail,
		Event: dag.LogEvent{
			TS:     at,
			RunID:  id,
			NodeID: id,
			Event:  dag.EventGrafted,
			Detail: fmt.Sprintf("fix node grafted onto %s from %s", joinIDs(parents), findingsRef),
		},
	}, nil
}

// singleGate returns the one gate every reviewed node belongs to.
func singleGate(st dag.State, parents []string) (string, error) {
	var gate string
	for _, p := range parents {
		g, ok := st.GateOf(p)
		if !ok {
			return "", fmt.Errorf("engine: reviewed node %s is not in the graph", p)
		}
		if gate == "" {
			gate = g
			continue
		}
		if gate != g {
			return "", fmt.Errorf("engine: reviewed nodes span gates %s and %s; a fix node belongs to exactly one", gate, g)
		}
	}
	return gate, nil
}

// singleKind returns the one deliverable kind every reviewed node produces,
// read from the detail records the caller loaded alongside the state.
func singleKind(details map[string]dag.Detail, parents []string) (dag.DeliverableKind, error) {
	var kind dag.DeliverableKind
	for _, p := range parents {
		d, ok := details[p]
		if !ok {
			return "", fmt.Errorf("engine: no detail record loaded for reviewed node %s", p)
		}
		if kind == "" {
			kind = d.DeliverableKind
			continue
		}
		if kind != d.DeliverableKind {
			return "", fmt.Errorf("engine: reviewed nodes mix deliverable kinds %s and %s; a fix node has exactly one", kind, d.DeliverableKind)
		}
	}
	return kind, nil
}

// unionSurface is every claim the reviewed nodes made, deduplicated. A fix
// may touch anything the work it corrects touched, and claiming less would
// let it be co-batched with something it can collide with.
func unionSurface(st dag.State, parents []string) []dag.Claim {
	seen := map[dag.Claim]bool{}
	var out []dag.Claim
	for _, p := range parents {
		n, ok := st.Node(p)
		if !ok {
			continue
		}
		for _, c := range n.Surface {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// joinIDs renders a node-id list for a human-readable sentence.
func joinIDs(list []string) string {
	switch len(list) {
	case 0:
		return "no nodes"
	case 1:
		return list[0]
	default:
		return fmt.Sprintf("%s and %d other node(s)", list[0], len(list)-1)
	}
}
