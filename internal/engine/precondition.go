package engine

import (
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/dispatch/routing"
)

// OperatorHold reports the operator gates standing in the way of an effort, or
// nil when none is.
//
// A gate is satisfied by its confirmation record and by nothing else, so two
// states count as held: an unsettled node waiting on a gate whose record is
// absent, incomplete, or made against another signal, and a gate the graph
// records as settled without one — being marked done is not being confirmed.
// Every held gate is named once, in graph order, so one report tells an
// operator everything they have to confirm.
//
// This is what separates a build waiting on a person from a build that cannot
// proceed at all: the first is a precondition and the second is a blockage, and
// only the second is something the plan has to be changed to fix.
func OperatorHold(st dag.State, details map[string]dag.Detail) error {
	gates := routing.Collect(details)
	if len(gates) == 0 {
		return nil
	}
	edges := declaredDeps(st)
	named := map[string]bool{}
	var held []routing.Unmet
	add := func(u routing.Unmet) {
		if named[u.Gate] {
			return
		}
		named[u.Gate] = true
		held = append(held, u)
	}
	for _, n := range st.Nodes() {
		if precondition, isGate := gates[n.ID]; isGate {
			if _, err := routing.Confirmed(precondition); err != nil && n.Status.Settled() {
				add(routing.Unmet{Gate: n.ID, Signal: precondition.Signal, Reason: err.Error()})
			}
			continue
		}
		if n.Status.Settled() {
			continue
		}
		for _, u := range routing.Blocking(edges, gates, n.ID) {
			add(u)
		}
	}
	if len(held) == 0 {
		return nil
	}
	return &routing.UnmetError{Subject: st.Project.ID, Gates: held}
}

// declaredEdges is a graph's dependency edges as its nodes declare them, which
// is all the gate-blocking walk reads — it needs neither an id scheme nor a
// built graph to answer what a node waits on.
type declaredEdges map[string][]string

func (e declaredEdges) Deps(id string) []string { return e[id] }

// declaredDeps indexes an effort's edges for the blocking walk.
func declaredDeps(st dag.State) declaredEdges {
	nodes := st.Nodes()
	out := make(declaredEdges, len(nodes))
	for _, n := range nodes {
		out[n.ID] = n.Deps
	}
	return out
}
