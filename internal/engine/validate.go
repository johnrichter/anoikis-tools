package engine

import (
	"fmt"
	"slices"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// Problem is one reason an effort is not ready to build.
type Problem struct {
	// Code names the rule that was broken, so a caller can branch on it.
	Code string `json:"code"`
	// Subject is the node, gate or field the problem is about.
	Subject string `json:"subject,omitempty"`
	// Message states the problem and what would fix it.
	Message string `json:"message"`
}

// Report is the readiness gate's verdict.
type Report struct {
	Problems []Problem `json:"problems,omitempty"`
	Nodes    int       `json:"nodes"`
	Gates    int       `json:"gates"`
	// Unbatchable counts nodes that can never be co-batched with anything,
	// because they leave at least one declared resource domain unclaimed —
	// silence in a domain is not the same as touching nothing in it. Such a
	// node is legal and simply runs alone, but a plan full of them is a
	// serial plan, which is worth saying out loud rather than discovering
	// from a build that never widens.
	Unbatchable int `json:"unbatchable"`
}

// OK reports whether the effort may be built.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Validate is the readiness gate: everything that must hold before an effort
// is built, checked in one pass so a plan is fixed once rather than one
// failure at a time.
//
// It checks the structural properties a build cannot recover from — cycles,
// dangling edges, ids the effort's own scheme rejects, gates a shard names
// but the policy does not declare — and the planning properties that would
// otherwise surface as a mid-build halt: a node with no route, a stage with
// no model, an empty or unprovable resource surface.
func Validate(st dag.State, h *policy.Harness, scheme ids.Scheme, details map[string]dag.Detail) (Report, error) {
	rep := Report{Nodes: len(st.Nodes()), Gates: len(st.Gates.Gates)}

	g, err := st.Graph(scheme)
	if err != nil {
		rep.Problems = append(rep.Problems, Problem{Code: "graph.unbuildable", Message: err.Error()})
		return rep, nil
	}
	if err := g.DetectCycles(); err != nil {
		rep.Problems = append(rep.Problems, Problem{Code: "graph.cycle", Message: err.Error()})
	}

	seen := map[string]bool{}
	for _, sh := range st.Shards {
		if _, ok := st.Gates.Find(sh.GateID); !ok {
			rep.Problems = append(rep.Problems, Problem{
				Code: "gate.undeclared", Subject: sh.GateID,
				Message: fmt.Sprintf("shard %s names a gate the gate policy does not declare", sh.GateID),
			})
		}
		for _, n := range sh.Nodes {
			if seen[n.ID] {
				rep.Problems = append(rep.Problems, Problem{
					Code: "node.duplicate", Subject: n.ID,
					Message: fmt.Sprintf("node %s appears in more than one shard; a node belongs to exactly one gate", n.ID),
				})
			}
			seen[n.ID] = true
			rep.Problems = append(rep.Problems, validateNode(h, scheme, n, details)...)
			if unbatchable(h, n) {
				rep.Unbatchable++
			}
		}
	}
	return rep, nil
}

// validateNode checks one node against the id scheme, the declared resource
// domains, and the route its deliverable kind selects.
func validateNode(h *policy.Harness, scheme ids.Scheme, n dag.Node, details map[string]dag.Detail) []Problem {
	var out []Problem
	if err := scheme.Validate(n.ID); err != nil {
		out = append(out, Problem{Code: "node.id", Subject: n.ID, Message: err.Error()})
	}
	if !n.Status.Known() {
		out = append(out, Problem{Code: "node.status", Subject: n.ID, Message: fmt.Sprintf("node %s declares unknown status %q", n.ID, n.Status)})
	}
	if !n.VerifyTier.Known() {
		out = append(out, Problem{Code: "node.verify_tier", Subject: n.ID, Message: fmt.Sprintf("node %s declares unknown verification tier %q", n.ID, n.VerifyTier)})
	}
	for _, c := range n.Surface {
		kind, declared := h.DomainKind(c.Domain)
		if !declared {
			out = append(out, Problem{
				Code: "surface.undeclared_domain", Subject: n.ID,
				Message: fmt.Sprintf("node %s claims resource domain %q, which this harness does not declare; nothing can prove it disjoint", n.ID, c.Domain),
			})
			continue
		}
		if kind == policy.DomainPath && !slices.Contains(policy.PathClaimKinds, c.Kind) {
			out = append(out, Problem{
				Code: "surface.claim_kind", Subject: n.ID,
				Message: fmt.Sprintf("node %s claims %q in path domain %q with kind %q; a path claim must be one of %v, or neither the disjointness proof nor the post-merge re-assertion can decide it", n.ID, c.Value, c.Domain, c.Kind, policy.PathClaimKinds),
			})
		}
	}
	if n.NeverDispatch {
		return out
	}
	if len(n.Surface) == 0 {
		out = append(out, Problem{
			Code: "surface.empty", Subject: n.ID,
			Message: fmt.Sprintf("node %s declares no resource surface; it can never be co-batched and its changes cannot be re-asserted after a merge", n.ID),
		})
	}
	detail, ok := details[n.ID]
	if !ok {
		out = append(out, Problem{Code: "node.detail_missing", Subject: n.ID, Message: fmt.Sprintf("node %s has no detail record at %s", n.ID, n.DetailRef)})
		return out
	}
	if !detail.DeliverableKind.Known() {
		out = append(out, Problem{
			Code: "node.deliverable_kind", Subject: n.ID,
			Message: fmt.Sprintf("node %s declares deliverable kind %q, which is outside the routed set %v", n.ID, detail.DeliverableKind, dag.AllKinds),
		})
		return out
	}
	if _, err := resolveStages(h, detail); err != nil {
		out = append(out, Problem{Code: "node.route", Subject: n.ID, Message: err.Error()})
	}
	return out
}

// unbatchable reports whether a node can never be proven disjoint from
// anything, because it leaves at least one declared domain unclaimed. A
// disjointness proof treats an unclaimed domain as unsafe rather than as
// empty, so registering a domain obliges every node to speak to it.
func unbatchable(h *policy.Harness, n dag.Node) bool {
	if len(n.Surface) == 0 {
		return true
	}
	for _, spec := range h.Surfaces {
		if !slices.ContainsFunc(n.Surface, func(c dag.Claim) bool { return c.Domain == spec.Name }) {
			return true
		}
	}
	return false
}
