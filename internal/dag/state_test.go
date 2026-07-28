package dag

import "testing"

// foldState builds a one-shard, one-node state with the given pre-fold
// status and log tail, the shape FoldLog reconciles.
func foldState(status Status, events ...LogEvent) State {
	return State{
		Shards: []Shard{{GateID: "g1", Nodes: []Node{{ID: "a", Status: status}}}},
		Events: events,
	}
}

func nodeStatus(t *testing.T, st State, id string) Status {
	t.Helper()
	n, ok := st.Node(id)
	if !ok {
		t.Fatalf("state has no node %q", id)
	}
	return n.Status
}

// TestFoldLogDispatchedThenFailedYieldsFailed pins the exact sequence
// SC-ANOIKIS-DECOUPLED found live: a run dispatched then failed must fold to
// failed, not sit at running because the switch never saw EventFailed.
func TestFoldLogDispatchedThenFailedYieldsFailed(t *testing.T) {
	st := foldState(StatusRunning,
		LogEvent{NodeID: "a", RunID: "r1", Event: EventDispatched},
		LogEvent{NodeID: "a", RunID: "r1", Event: EventFailed},
	).FoldLog()

	if got := nodeStatus(t, st, "a"); got != StatusFailed {
		t.Errorf("status after dispatched-then-failed = %s, want %s", got, StatusFailed)
	}
}

// TestFoldLogLeavesASettledNodeAlone confirms the log-wins contract stops at
// a merge: nothing after that point can undo it, including a failed event a
// stale idempotent replay might carry.
func TestFoldLogLeavesASettledNodeAlone(t *testing.T) {
	st := foldState(StatusDone,
		LogEvent{NodeID: "a", RunID: "r1", Event: EventFailed},
	).FoldLog()

	if got := nodeStatus(t, st, "a"); got != StatusDone {
		t.Errorf("status of a settled node after folding = %s, want %s unchanged", got, StatusDone)
	}
}

// TestFoldLogCompleteStaysAtWhateverDispatchFolded pins EventComplete's
// documented no-op: a pass recorded but not yet merged stays running, the
// status its own dispatch already folded to.
func TestFoldLogCompleteStaysAtWhateverDispatchFolded(t *testing.T) {
	st := foldState(StatusReady,
		LogEvent{NodeID: "a", RunID: "r1", Event: EventDispatched},
		LogEvent{NodeID: "a", RunID: "r1", Event: EventComplete},
	).FoldLog()

	if got := nodeStatus(t, st, "a"); got != StatusRunning {
		t.Errorf("status after dispatched-then-complete = %s, want %s", got, StatusRunning)
	}
}

// TestFoldLogGraftedDoesNotTouchTheShardStatus pins EventGrafted's documented
// no-op: the grafted node's status came from the graft that inserted it into
// the shard, and the log entry beside it is provenance, not a fold input.
func TestFoldLogGraftedDoesNotTouchTheShardStatus(t *testing.T) {
	st := foldState(StatusReady,
		LogEvent{NodeID: "a", RunID: "a", Event: EventGrafted},
	).FoldLog()

	if got := nodeStatus(t, st, "a"); got != StatusReady {
		t.Errorf("status of a grafted node after folding = %s, want %s unchanged", got, StatusReady)
	}
}

// TestFoldLogEventDecisionsAreExhaustive is the guard against the defect
// class, not just its one instance: every member of the run-log event enum
// must be either folded to a status or listed in foldNoOps with a reason.
// A member added to AllEvents without updating either map fails here instead
// of folding as a silent no-op.
func TestFoldLogEventDecisionsAreExhaustive(t *testing.T) {
	for _, e := range AllEvents {
		_, folds := foldStatus[e]
		_, noop := foldNoOps[e]
		switch {
		case !folds && !noop:
			t.Errorf("event %q is neither folded to a status nor documented as a deliberate no-op", e)
		case folds && noop:
			t.Errorf("event %q is both folded and marked a no-op; it must be exactly one", e)
		}
	}
	if got, want := len(foldStatus)+len(foldNoOps), len(AllEvents); got != want {
		t.Errorf("foldStatus and foldNoOps together cover %d events, but AllEvents has %d; check for an entry outside the enum", got, want)
	}
}
