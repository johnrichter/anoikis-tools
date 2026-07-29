package dag

import (
	"fmt"
	"slices"

	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// State is a whole effort held in memory: the manifest, every shard, the gate
// policy, and the run log's tail. It is the single input to every engine
// decision, which is what makes those decisions reproducible from a captured
// state alone.
type State struct {
	Project Project
	Index   Index
	Shards  []Shard
	Gates   Gates
	// Events are the run log from the resume cursor onward, which is every
	// transition not already folded into the shards' own status fields.
	Events []LogEvent
	// LayerFloor is the sequence number the next layer takes when the log's
	// tail carries no dispatch, because the layers before it were sealed at
	// the cursor. Without it, sealing the log would restart numbering and two
	// different layers would answer to one sequence.
	LayerFloor int
}

// Nodes returns every node across every shard, shard order then node order.
func (s State) Nodes() []Node {
	var out []Node
	for _, sh := range s.Shards {
		out = append(out, sh.Nodes...)
	}
	return out
}

// Node returns the node with the given id.
func (s State) Node(id string) (Node, bool) {
	for _, sh := range s.Shards {
		for _, n := range sh.Nodes {
			if n.ID == id {
				return n, true
			}
		}
	}
	return Node{}, false
}

// GateOf returns the id of the gate whose shard holds the node.
func (s State) GateOf(nodeID string) (string, bool) {
	for _, sh := range s.Shards {
		for _, n := range sh.Nodes {
			if n.ID == nodeID {
				return sh.GateID, true
			}
		}
	}
	return "", false
}

// Graph builds the dependency graph over every node, keyed by node id and
// rendered through the effort's id scheme. Node insertion follows shard then
// node order, which is the tie-break behind every ordered result the graph
// then produces — so two runs over the same state schedule identically.
//
// A dependency naming a node the graph does not hold is reported here rather
// than being dropped: a dangling edge is a plan defect, not a node with one
// fewer prerequisite.
func (s State) Graph(scheme ids.Scheme) (*graph.Graph[string, Node], error) {
	g := graph.New[string, Node](ids.GraphScheme(scheme))
	for _, sh := range s.Shards {
		for _, n := range sh.Nodes {
			if err := g.AddNode(n.ID, n); err != nil {
				return nil, err
			}
		}
	}
	for _, sh := range s.Shards {
		for _, n := range sh.Nodes {
			for _, dep := range n.Deps {
				if err := g.AddDep(n.ID, dep); err != nil {
					return nil, fmt.Errorf("node %s: %w", n.ID, err)
				}
			}
		}
	}
	return g, nil
}

// Ready returns the nodes eligible to be dispatched right now, in graph
// insertion order: not settled, not running, not failed-out, every
// dependency settled, and dispatchable at all.
//
// A node marked never_dispatch is structurally excluded — it is work the
// engine performs itself, and handing it to an agent is refused here rather
// than being caught downstream.
func (s State) Ready(g *graph.Graph[string, Node]) []string {
	var out []string
	for _, id := range g.IDs() {
		n, ok := g.Node(id)
		if !ok || n.NeverDispatch {
			continue
		}
		if n.Status.Settled() || n.Status == StatusRunning {
			continue
		}
		if n.Status == StatusFailed && !n.RetriesLeft() {
			continue
		}
		if !depsSettled(g, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// depsSettled reports whether every dependency of id is merged.
func depsSettled(g *graph.Graph[string, Node], id string) bool {
	for _, dep := range g.Deps(id) {
		d, ok := g.Node(dep)
		if !ok || !d.Status.Settled() {
			return false
		}
	}
	return true
}

// Running returns the ids of nodes currently dispatched, in graph order.
func (s State) Running() []string {
	var out []string
	for _, n := range s.Nodes() {
		if n.Status == StatusRunning {
			out = append(out, n.ID)
		}
	}
	return out
}

// Exhausted returns the ids of nodes that failed with no attempt left, in
// graph order. One of these halts the build for an operator replan.
func (s State) Exhausted() []string {
	var out []string
	for _, n := range s.Nodes() {
		if n.Status == StatusFailed && !n.RetriesLeft() {
			out = append(out, n.ID)
		}
	}
	return out
}

// ReachedGate returns the first gate whose shard is fully settled and which
// the build has not yet been let past — the boundary it has arrived at, with
// its review or its merge still outstanding.
func (s State) ReachedGate() (Gate, bool) {
	for _, sh := range s.Shards {
		gate, ok := s.Gates.Find(sh.GateID)
		if !ok || gate.Closed() {
			continue
		}
		if allSettled(sh.Nodes) {
			return gate, true
		}
	}
	return Gate{}, false
}

// Reached reports whether every node in a gate's shard is merged, which is
// what it means for the build to have arrived at that gate.
func (s State) Reached(gateID string) bool {
	for _, sh := range s.Shards {
		if sh.GateID == gateID {
			return allSettled(sh.Nodes)
		}
	}
	return false
}

// Complete reports whether every node is settled and every gate is closed.
func (s State) Complete() bool {
	for _, sh := range s.Shards {
		if !allSettled(sh.Nodes) {
			return false
		}
	}
	for _, g := range s.Gates.Gates {
		if !g.Closed() {
			return false
		}
	}
	return true
}

// allSettled reports whether every node in the slice is merged.
func allSettled(nodes []Node) bool {
	for _, n := range nodes {
		if !n.Status.Settled() {
			return false
		}
	}
	return true
}

// foldStatus is the status FoldLog assigns a node for each event that moves
// it. Every other member of the event enum is a documented no-op in
// foldNoOps; together the two must cover AllEvents exactly, so a member
// added to the enum without a folding decision is caught rather than folding
// as a silent no-op.
var foldStatus = map[Event]Status{
	EventDispatched: StatusRunning,
	EventFailed:     StatusFailed,
	EventMerged:     StatusDone,
}

// foldNoOps are the event enum members FoldLog deliberately does not fold,
// with the reason each is safe to leave out of foldStatus.
var foldNoOps = map[Event]string{
	// A pass is recorded as complete but stays unmerged until the layer's
	// merge lands; the model represents that as running, the status the
	// node's own dispatched event already folded to. See engine.Apply.
	EventComplete: "complete-but-unmerged is represented as running, already folded from the node's dispatch",
	// Grafted records the insertion of a fix node, not a transition of an
	// existing one: the node it names is written into its shard with its
	// starting status by the graft that inserts it, and the log entry beside
	// it is provenance, not a fold input.
	EventGrafted: "records a graph mutation, not a status transition; the grafted node's status is written to its shard directly",
}

// FoldLog applies the log tail's transitions to the shards' status fields and
// returns the reconciled state.
//
// The log records what happened; a shard is where that record is kept for
// scheduling. A process killed between journalling a transition and rewriting
// the shard leaves the two disagreeing, and only one of them is append-only —
// so the log wins. Folding it on load is what makes that window survivable: a
// node the log says was dispatched is running, one it says failed is failed,
// and one it says was merged is done, whatever its shard still says. A node
// already settled is left alone, since nothing in the log can undo a merge.
func (s State) FoldLog() State {
	if len(s.Events) == 0 {
		return s
	}
	status := map[string]Status{}
	for _, e := range s.Events {
		if folded, ok := foldStatus[e.Event]; ok {
			status[e.NodeID] = folded
		}
	}
	shards := make([]Shard, 0, len(s.Shards))
	for _, sh := range s.Shards {
		next := sh
		next.Nodes = slices.Clone(sh.Nodes)
		for i, n := range next.Nodes {
			folded, ok := status[n.ID]
			if !ok || n.Status.Settled() {
				continue
			}
			n.Status = folded
			next.Nodes[i] = n
		}
		shards = append(shards, next)
	}
	s.Shards = shards
	return s
}

// LatestEvents reduces the run log to the last event recorded per run id,
// which is the only view resume needs. Order follows first appearance, so a
// resume plan lists runs in the order they were dispatched.
func (s State) LatestEvents() []LogEvent {
	index := map[string]int{}
	var out []LogEvent
	for _, e := range s.Events {
		if i, ok := index[e.RunID]; ok {
			out[i] = e
			continue
		}
		index[e.RunID] = len(out)
		out = append(out, e)
	}
	return out
}

// NextLayerSeq returns the sequence number the next dispatched layer takes:
// one past the highest the log's tail carries, or the floor the cursor sealed
// when that tail holds no dispatch at all.
func (s State) NextLayerSeq() int {
	next := s.LayerFloor
	for _, e := range s.Events {
		if e.Event == EventDispatched && e.LayerSeq >= next {
			next = e.LayerSeq + 1
		}
	}
	return next
}

// CurrentLayerSeq returns the sequence number of the layer most recently
// dispatched — the one whose results are recorded next.
func (s State) CurrentLayerSeq() int { return max(s.NextLayerSeq()-1, 0) }

// GraftOrdinal counts the grafts already recorded against a node, so a
// derived id stays unique across repeated fix verdicts on the same work.
func (s State) GraftOrdinal(parents []string) int {
	n := 0
	for _, node := range s.Nodes() {
		if len(node.GraftedFrom) > 0 && slices.Equal(node.GraftedFrom, parents) {
			n++
		}
	}
	return n
}
