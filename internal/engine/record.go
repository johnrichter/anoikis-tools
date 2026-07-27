package engine

import (
	"fmt"
	"slices"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// Outcome is one node's returned result, as the driver hands it back.
type Outcome struct {
	Result dag.RunResult `json:"result"`
	// Usage is what the run cost, from the spend provider. It is never
	// self-reported by the run itself.
	Usage dag.Usage `json:"usage"`
	// ResultRef is where the result was stored, journalled with the run's
	// transition so an audit reaches the run's output from the log alone.
	ResultRef string `json:"result_ref,omitempty"`
}

// RaisedFinding is one observation a run reported, carrying the node it came
// from so the register can be told which node's rollup to cite it on.
type RaisedFinding struct {
	NodeID string          `json:"node_id"`
	Seed   dag.FindingSeed `json:"seed"`
}

// Recording is the state change one batch of outcomes produces, split into
// the two moments a merge sits between.
//
// Apply is everything true the instant a run returns: statuses moved off
// running, results recorded, findings raised. Mergeable is the set that then
// goes onto the build branch. Only after that merge succeeds does a node
// become done, because done means merged — a later node branching from the
// build branch must contain its dependencies' work.
type Recording struct {
	// Shards carry the applied statuses, ready to persist.
	Shards []dag.Shard
	// Events are the transitions to append to the run log, in order.
	Events []dag.LogEvent
	// Mergeable names the nodes whose work should now be merged onto the
	// build branch.
	Mergeable []string
	// Runs maps each node in this batch to the run that produced its outcome,
	// so the merge that settles it journals the same run identity the dispatch
	// did and a resume still sees one run rather than two.
	Runs map[string]string
	// Failed names the nodes that did not pass.
	Failed []string
	// Findings are the observations the runs raised, for the register.
	Findings []RaisedFinding
	// FixVerdicts names the nodes a review returned the fix verdict for; each
	// becomes a grafted fix node.
	FixVerdicts []string
	// Spend is the batch's total, unknown as a whole if any run was unpriced.
	Spend dag.Usage
	// Unpriced counts the runs the provider could not price, so the effort's
	// own total can record how much of it is unmeasured rather than absorbing
	// the gap.
	Unpriced int
}

// Apply folds a batch of outcomes into the graph.
//
// A run that passed moves to complete-but-unmerged, which this model
// represents by leaving the node running until the merge settles it; a run
// that failed moves to failed and burns an attempt. Neither transition is a
// judgement — the run itself already reported pass or fail, and a verdict
// outside the harness's declared vocabulary is refused rather than coerced
// into one.
//
// Applying the same outcome twice is a no-op. A run the log already records
// as finished burns no second attempt, journals no second transition and
// raises no second copy of its findings — it is still reported as mergeable,
// because the reason a caller is applying it again is almost always that the
// merge behind it did not land. That is what makes recovery after an
// interrupted or refused merge a matter of simply running the same command
// again.
func Apply(st dag.State, h *policy.Harness, outcomes []Outcome, layerSeq int, at string) (Recording, error) {
	byNode := map[string]Outcome{}
	for _, o := range outcomes {
		if o.Result.NodeID == "" {
			return Recording{}, fmt.Errorf("engine: an outcome carries no node id")
		}
		if o.Result.Status != dag.RunPass && o.Result.Status != dag.RunFail {
			return Recording{}, fmt.Errorf("engine: node %s returned unrecognised run status %q", o.Result.NodeID, o.Result.Status)
		}
		if o.Result.Verdict != "" && !h.KnownVerdict(o.Result.Verdict) {
			return Recording{}, fmt.Errorf("engine: node %s returned verdict %q, which is not in this harness's declared vocabulary", o.Result.NodeID, o.Result.Verdict)
		}
		if _, ok := st.Node(o.Result.NodeID); !ok {
			return Recording{}, fmt.Errorf("engine: outcome names unknown node %s", o.Result.NodeID)
		}
		byNode[o.Result.NodeID] = o
	}

	settled := settledRuns(st)
	rec := Recording{Spend: dag.Usage{Known: true}, Runs: map[string]string{}}
	rec.Shards = make([]dag.Shard, 0, len(st.Shards))
	for _, sh := range st.Shards {
		next := sh
		next.Nodes = slices.Clone(sh.Nodes)
		for i, n := range next.Nodes {
			o, ok := byNode[n.ID]
			if !ok {
				continue
			}
			if n.Status.Settled() {
				// The node's work is already merged. Re-applying an outcome
				// to it would burn an attempt and re-raise its findings
				// against work nobody re-ran.
				continue
			}
			rec.Runs[n.ID] = o.Result.RunID
			switch settled[o.Result.RunID] {
			case dag.EventMerged:
				continue
			case dag.EventComplete:
				rec.Mergeable = append(rec.Mergeable, n.ID)
				continue
			case dag.EventFailed:
				rec.Failed = append(rec.Failed, n.ID)
				continue
			}
			n.Attempts++
			switch o.Result.Status {
			case dag.RunPass:
				rec.Mergeable = append(rec.Mergeable, n.ID)
				rec.Events = append(rec.Events, event(dag.EventComplete, o, layerSeq, at))
			default:
				n.Status = dag.StatusFailed
				rec.Failed = append(rec.Failed, n.ID)
				rec.Events = append(rec.Events, event(dag.EventFailed, o, layerSeq, at))
			}
			next.Nodes[i] = n
			rec.Findings = append(rec.Findings, attribute(n.ID, o.Result.Findings)...)
			if h.Gates.FixVerdict != "" && o.Result.Verdict == h.Gates.FixVerdict {
				rec.FixVerdicts = append(rec.FixVerdicts, n.ID)
			}
			rec.Spend = rec.Spend.Fold(o.Usage)
			if !o.Usage.Known {
				rec.Unpriced++
			}
		}
		rec.Shards = append(rec.Shards, next)
	}
	return rec, nil
}

// settledRuns reduces the run log to the latest recorded event per run id, for
// the runs whose work is already accounted for.
func settledRuns(st dag.State) map[string]dag.Event {
	out := map[string]dag.Event{}
	for _, e := range st.LatestEvents() {
		switch e.Event {
		case dag.EventComplete, dag.EventFailed, dag.EventMerged:
			out[e.RunID] = e.Event
		}
	}
	return out
}

// attribute stamps each finding with the node that raised it, so two nodes
// reporting the same observation stay two entries in the register while one
// node reporting it twice stays one.
func attribute(nodeID string, seeds []dag.FindingSeed) []RaisedFinding {
	out := make([]RaisedFinding, 0, len(seeds))
	for _, s := range seeds {
		s.Statement = nodeID + ": " + s.Statement
		out = append(out, RaisedFinding{NodeID: nodeID, Seed: s})
	}
	return out
}

// Settle marks the nodes a layer merge landed as done and journals a merged
// event for each, under the same run identity that dispatched them. done means
// merged, so this is the only place a node reaches it.
func Settle(shards []dag.Shard, merged []string, runs map[string]string, layerSeq int, at string) ([]dag.Shard, []dag.LogEvent) {
	set := map[string]bool{}
	for _, id := range merged {
		set[id] = true
	}
	out := make([]dag.Shard, 0, len(shards))
	var events []dag.LogEvent
	for _, sh := range shards {
		next := sh
		next.Nodes = slices.Clone(sh.Nodes)
		for i, n := range next.Nodes {
			if !set[n.ID] {
				continue
			}
			n.Status = dag.StatusDone
			next.Nodes[i] = n
			events = append(events, dag.LogEvent{
				TS:       at,
				RunID:    runs[n.ID],
				NodeID:   n.ID,
				Event:    dag.EventMerged,
				LayerSeq: layerSeq,
			})
		}
		out = append(out, next)
	}
	return out, events
}

// Closure is one merged node's retirement: where its detail record moved to,
// and the tombstone that stands in for it in the hot shard.
type Closure struct {
	NodeID    string
	DetailRef string
	Tombstone dag.Tombstone
}

// Retire replaces each closed node's hot record with its tombstone.
//
// A node's full detail moves out of the scheduling path once its work is
// merged, but the node itself stays in its shard: a live node whose dependency
// has been archived must still resolve that dependency as settled, and a
// tombstone is what lets it. The node keeps its declared surface, because a
// fix node grafted later claims the union of what the work it corrects
// claimed.
func Retire(shards []dag.Shard, closures []Closure) []dag.Shard {
	byNode := make(map[string]Closure, len(closures))
	for _, c := range closures {
		byNode[c.NodeID] = c
	}
	out := make([]dag.Shard, 0, len(shards))
	for _, sh := range shards {
		next := sh
		next.Nodes = slices.Clone(sh.Nodes)
		for i, n := range next.Nodes {
			c, ok := byNode[n.ID]
			if !ok {
				continue
			}
			n.Status = dag.StatusArchived
			n.DetailRef = c.DetailRef
			tombstone := c.Tombstone
			n.Tombstone = &tombstone
			next.Nodes[i] = n
		}
		out = append(out, next)
	}
	return out
}

// MarkRunning moves the dispatched nodes to running, which is what stops the
// next Step from launching them again.
func MarkRunning(shards []dag.Shard, dispatched []string) []dag.Shard {
	set := map[string]bool{}
	for _, id := range dispatched {
		set[id] = true
	}
	out := make([]dag.Shard, 0, len(shards))
	for _, sh := range shards {
		next := sh
		next.Nodes = slices.Clone(sh.Nodes)
		for i, n := range next.Nodes {
			if set[n.ID] {
				n.Status = dag.StatusRunning
				next.Nodes[i] = n
			}
		}
		out = append(out, next)
	}
	return out
}

// event renders one outcome as a run-log entry.
func event(kind dag.Event, o Outcome, layerSeq int, at string) dag.LogEvent {
	usage := o.Usage
	return dag.LogEvent{
		TS:           at,
		RunID:        o.Result.RunID,
		NodeID:       o.Result.NodeID,
		Event:        kind,
		LayerSeq:     layerSeq,
		RunResultRef: o.ResultRef,
		Usage:        &usage,
	}
}
