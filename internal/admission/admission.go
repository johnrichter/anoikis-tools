// Package admission decides which ready nodes may run at the same time.
//
// The decision rests on two independent facts, both supplied by the graph
// library: are the nodes independent in the dependency graph, and are their
// declared resource surfaces provably disjoint. This package adds the policy
// that sits above those proofs — batch order, the group bound, and the
// guarantee that a batch is never empty — and records, for every node it
// turned away, which node blocked it and on what ground.
//
// # Biased unsafe unless proven disjoint
//
// A pair is co-batched only on an explicit disjoint verdict. An undeclared
// domain, an unregistered one, a claim no domain can reason about, an empty
// surface, or any other verdict all keep the pair apart. Failing this way
// costs parallelism; failing the other way destroys work that already ran.
//
// # Batch of one is the floor, not a failure
//
// The first ready node is always admitted, so a node that can co-batch with
// nothing still runs — alone. There is no state in which the engine has ready
// work and admits none of it.
//
// # What the proof cannot see
//
// A surface is a declaration. A node that writes outside what it declared,
// two nodes emitting the same new symbol into one package, two paths aliased
// by a symlink — none of those are visible to any proof over declared text,
// and no bias here helps. That residual belongs to the post-merge backstop
// that compiles the merged result and re-asserts what each node actually
// touched, which is why the backstop always runs and is not a policy choice.
package admission

import (
	"fmt"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// Deferral records one node held out of a batch, and why.
type Deferral struct {
	// Node is the node that was not admitted.
	Node string `json:"node"`
	// Blocker is the already-admitted member the node could not join, empty
	// when the batch was simply full.
	Blocker string `json:"blocker,omitempty"`
	// Verdict is the graph library's own account of the pair, so an
	// admission decision can be re-derived and audited after the fact.
	Verdict graph.Verdict `json:"verdict"`
}

// Batch is one admission decision: who runs together now, and who waits.
type Batch struct {
	// Members are the nodes admitted, in graph insertion order. Never empty
	// when there was ready work.
	Members []string `json:"members"`
	// Deferred are the ready nodes held back, each with its reason.
	Deferred []Deferral `json:"deferred,omitempty"`
	// SoloFallback is true when only one node was admitted while others were
	// ready — the batch-of-one floor, worth surfacing because it is the
	// difference between a wide layer and a serial one.
	SoloFallback bool `json:"solo_fallback,omitempty"`
}

// ErrNoReady reports an admission call with nothing to admit.
var ErrNoReady = fmt.Errorf("admission: no ready nodes")

// Admit selects the widest provably safe batch from ready, in the order the
// graph holds those nodes.
//
// A node joins only when every already-admitted member co-batches with it:
// independent in the dependency graph and surface-disjoint under p. The first
// ready node is admitted unconditionally, so the result always carries at
// least one member.
//
// maxGroup bounds the batch; nodes turned away for that reason are deferred
// on the group-full ground rather than silently dropped.
func Admit(g *graph.Graph[string, dag.Node], ready []string, p *graph.Prover, maxGroup int) (Batch, error) {
	if len(ready) == 0 {
		return Batch{}, ErrNoReady
	}
	if p == nil {
		return Batch{}, graph.ErrNoProver
	}
	if maxGroup < 1 {
		maxGroup = 1
	}

	surfaces := func(id string) graph.Surface {
		n, ok := g.Node(id)
		if !ok {
			return nil
		}
		return n.ResourceSurface()
	}

	var b Batch
	for _, id := range ready {
		if len(b.Members) == 0 {
			b.Members = append(b.Members, id)
			continue
		}
		if len(b.Members) >= maxGroup {
			b.Deferred = append(b.Deferred, Deferral{
				Node:    id,
				Verdict: graph.Verdict{Relation: graph.RelationUnknown, Ground: graph.GroundGroupFull},
			})
			continue
		}
		if blocker, verdict, ok := admits(g, id, b.Members, surfaces, p); !ok {
			b.Deferred = append(b.Deferred, Deferral{Node: id, Blocker: blocker, Verdict: verdict})
			continue
		}
		b.Members = append(b.Members, id)
	}
	b.SoloFallback = len(b.Members) == 1 && len(b.Deferred) > 0
	return b, nil
}

// admits reports whether id can join every current member, naming the first
// member that refuses it and that pair's verdict.
func admits(g *graph.Graph[string, dag.Node], id string, members []string, surfaces func(string) graph.Surface, p *graph.Prover) (string, graph.Verdict, bool) {
	for _, m := range members {
		ok, verdict := g.CanCoBatch(id, m, surfaces, p)
		if !ok {
			return m, verdict, false
		}
	}
	return "", graph.Verdict{Relation: graph.RelationDisjoint, Ground: graph.GroundProved}, true
}
