package engine

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/ids"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// Closing is what feeding a gate's review verdict back does to that gate.
type Closing struct {
	// Status is where the gate stands afterwards: passed, or back under
	// review while the fix it asked for is built.
	Status dag.GateStatus `json:"status"`
	// Grafts are the fix nodes the verdict calls for, one per deliverable
	// kind the gate covers.
	Grafts []Graft `json:"grafts,omitempty"`
	// Reviewed names the nodes the verdict was returned against.
	Reviewed []string `json:"reviewed"`
}

// CloseGate decides what a gate's review verdict means for the build.
//
// A gate that declares a review is closed only by a verdict from the harness's
// own vocabulary; a gate that declares none closes without one. The fix
// verdict is not a judgement to make here — the review already made it — so it
// mechanically grafts the fix work and leaves the gate open until that work is
// merged. Every other declared verdict passes the gate.
//
// A fix spanning several deliverable kinds becomes one fix node per kind:
// grouping is mechanical, whereas inventing a single route for mixed work
// would be a judgement.
func CloseGate(st dag.State, h *policy.Harness, scheme ids.Scheme, details map[string]dag.Detail, gate dag.Gate, verdict, findingsRef, at string) (Closing, error) {
	if !st.Reached(gate.ID) {
		return Closing{}, fmt.Errorf("engine: gate %s still has unmerged work; a boundary is closed once the build arrives at it, not before", gate.ID)
	}
	if verdict == "" {
		if gate.NeedsReview() {
			return Closing{}, fmt.Errorf("engine: gate %s declares a %s deep review; it closes on a verdict from %s, not on being reached", gate.ID, gate.Policy.DeepReview, strings.Join(h.Gates.Verdicts, "|"))
		}
		return Closing{Status: dag.GatePassed}, nil
	}
	if !h.KnownVerdict(verdict) {
		return Closing{}, fmt.Errorf("engine: verdict %q is not in this harness's declared vocabulary %s", verdict, strings.Join(h.Gates.Verdicts, "|"))
	}

	reviewed := reviewedNodes(st, gate.ID)
	if verdict != h.Gates.FixVerdict {
		return Closing{Status: dag.GatePassed, Reviewed: reviewed}, nil
	}
	if findingsRef == "" {
		return Closing{}, fmt.Errorf("engine: the %s verdict seeds its fix node from the review's findings artifact; name it", verdict)
	}
	if len(reviewed) == 0 {
		return Closing{}, fmt.Errorf("engine: gate %s holds no dispatched node for a fix to depend on", gate.ID)
	}

	closing := Closing{Status: dag.GateReviewing, Reviewed: reviewed}
	for _, group := range byKind(details, reviewed) {
		graft, err := PlanGraft(st, h, scheme, details, group, findingsRef, at)
		if err != nil {
			return Closing{}, err
		}
		closing.Grafts = append(closing.Grafts, graft)
	}
	return closing, nil
}

// reviewedNodes returns the nodes a gate's review covers: everything in its
// shard that was dispatched to an agent, in shard order. A node the engine
// performs itself contributed nothing to the diff under review.
func reviewedNodes(st dag.State, gateID string) []string {
	var out []string
	for _, sh := range st.Shards {
		if sh.GateID != gateID {
			continue
		}
		for _, n := range sh.Nodes {
			if !n.NeverDispatch {
				out = append(out, n.ID)
			}
		}
	}
	return out
}

// byKind groups reviewed nodes by the deliverable kind they produced, kind
// order sorted so a gate closes identically however its shard is ordered.
func byKind(details map[string]dag.Detail, reviewed []string) [][]string {
	groups := map[dag.DeliverableKind][]string{}
	for _, id := range reviewed {
		groups[details[id].DeliverableKind] = append(groups[details[id].DeliverableKind], id)
	}
	out := make([][]string, 0, len(groups))
	for _, kind := range slices.Sorted(maps.Keys(groups)) {
		out = append(out, groups[kind])
	}
	return out
}
