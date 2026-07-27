package engine_test

import (
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/engine"
	"github.com/johnrichter/anoikis-tools/internal/ids"
)

// reachedGate is a one-gate effort whose work is all merged, which is the only
// state a gate is ever closed from.
func reachedGate(t *testing.T, deepReview string) (dag.State, map[string]dag.Detail) {
	t.Helper()
	st := state([]dag.Node{node("a", "svc/a", dag.StatusDone), node("b", "svc/b", dag.StatusDone)}, dag.GatePending)
	st.Gates.Gates[0].Policy.DeepReview = deepReview
	return st, map[string]dag.Detail{
		"a": {ID: "a", DeliverableKind: dag.KindCode},
		"b": {ID: "b", DeliverableKind: dag.KindCode},
	}
}

func closeGate(t *testing.T, st dag.State, details map[string]dag.Detail, verdict, findingsRef string) engine.Closing {
	t.Helper()
	closing, err := engine.CloseGate(st, harness(t), ids.Opaque{}, details, st.Gates.Gates[0], verdict, findingsRef, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("close gate: %v", err)
	}
	return closing
}

func TestAReviewedGateNeedsItsVerdict(t *testing.T) {
	st, details := reachedGate(t, "batched")
	if _, err := engine.CloseGate(st, harness(t), ids.Opaque{}, details, st.Gates.Gates[0], "", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("a gate declaring a deep review closed without one")
	}
}

func TestAnUnreviewedGateClosesWithoutAVerdict(t *testing.T) {
	st, details := reachedGate(t, dag.DeepReviewNone)
	if got := closeGate(t, st, details, "", "").Status; got != dag.GatePassed {
		t.Fatalf("status = %s, want passed", got)
	}
}

func TestAnUndeclaredVerdictIsRefused(t *testing.T) {
	st, details := reachedGate(t, "batched")
	if _, err := engine.CloseGate(st, harness(t), ids.Opaque{}, details, st.Gates.Gates[0], "looks-fine", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("a verdict outside the harness's declared vocabulary closed a gate")
	}
}

func TestThePassVerdictPassesTheGate(t *testing.T) {
	st, details := reachedGate(t, "batched")
	closing := closeGate(t, st, details, "pass", "")
	if closing.Status != dag.GatePassed {
		t.Fatalf("status = %s, want passed", closing.Status)
	}
	if len(closing.Grafts) != 0 {
		t.Errorf("a passing review grafted %d node(s)", len(closing.Grafts))
	}
}

func TestTheFixVerdictGraftsAndHoldsTheGateOpen(t *testing.T) {
	st, details := reachedGate(t, "batched")
	closing := closeGate(t, st, details, "fix", "results/review.json")
	if closing.Status == dag.GatePassed {
		t.Fatal("a fix verdict passed the gate; the fix it asked for has not been built")
	}
	if len(closing.Grafts) != 1 {
		t.Fatalf("grafts = %d, want one per deliverable kind under review", len(closing.Grafts))
	}
	if got := closing.Grafts[0].Node.Deps; len(got) != 2 {
		t.Errorf("the fix depends on %v, want every reviewed node", got)
	}
}

func TestTheFixVerdictSplitsByDeliverableKind(t *testing.T) {
	st, details := reachedGate(t, "batched")
	details["b"] = dag.Detail{ID: "b", DeliverableKind: dag.KindDocs}
	closing := closeGate(t, st, details, "fix", "results/review.json")
	if len(closing.Grafts) != 2 {
		t.Fatalf("grafts = %d, want one per deliverable kind, since one node cannot be routed two ways", len(closing.Grafts))
	}
}

func TestAGateWithUnmergedWorkIsNotClosable(t *testing.T) {
	st, details := reachedGate(t, "batched")
	st.Shards[0].Nodes[1].Status = dag.StatusReady
	if _, err := engine.CloseGate(st, harness(t), ids.Opaque{}, details, st.Gates.Gates[0], "pass", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("a gate closed over work the build has not finished")
	}
}

func TestTheFixVerdictNeedsTheReviewsFindings(t *testing.T) {
	st, details := reachedGate(t, "batched")
	if _, err := engine.CloseGate(st, harness(t), ids.Opaque{}, details, st.Gates.Gates[0], "fix", "", "2026-01-01T00:00:00Z"); err == nil {
		t.Fatal("a fix node was planned with nothing to seed it")
	}
}
