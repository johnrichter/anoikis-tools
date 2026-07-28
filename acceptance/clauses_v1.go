package acceptance

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/admission"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/dispatch/boundary"
	"github.com/johnrichter/anoikis-tools/internal/ids"
)

// The first version's cost and context gates are stated by their source as
// measurements taken over a real driven build: compactions counted, cache
// writes trended, returns and tool calls observed. None of them can be settled
// by reading a checkout, and the switchover puts this gate before that build —
// so each is held here to the mechanism that makes its measurement possible
// and honest, and the measurement itself leaves as an open condition. What each
// one still owes is on the clause, in its own words.

// defaultContextWindow is the window every node runs at in this version. A
// wider one is a per-node decision taken on evidence, and there is no evidence
// yet.
const defaultContextWindow = "200k"

// pointerReturnGate is the figure the pointer-return gate names, in bytes.
const pointerReturnGate = 2048

// v1GateClauses are the first version's cost and context gates.
func v1GateClauses() []Clause {
	return []Clause{{
		ID:       "v1/orchestrator-holds-pointers-only",
		Source:   SourceV1Gates,
		Bar:      BarMechanism,
		Requires: "A full build forces no compaction on the driving session.",
		Asserts:  "A directive carries node identities and never node detail: what a node is for, what would make it acceptable and what it produced are reachable only through a reference the driver resolves when it dispatches.",
		Measured: "the number of forced compactions over a real driven build, counted by the driving harness itself.",
		check:    orchestratorHoldsPointersOnly,
	}, {
		ID:       "v1/directive-size-is-flat-in-completed-work",
		Source:   SourceV1Gates,
		Bar:      BarMechanism,
		Requires: "The driving session's per-turn cache writes stay flat as completed nodes accumulate, rather than growing with them.",
		Asserts:  "The directive for the same next action is byte-identical whether the graph holds one completed node or fifty, so nothing the driver receives grows with the work already done.",
		Measured: "the per-turn cache-write trend against completed-node count over a real driven build.",
		check:    directiveSizeIsFlat,
	}, {
		ID:       "v1/returns-are-bounded-pointers",
		Source:   SourceV1Gates,
		Bar:      BarMechanism,
		Requires: "Every return from a dispatched run is a pointer, at or below the two-kilobyte figure the gate names.",
		Asserts:  "Every return class declares a byte ceiling in one place; an over-length return is refused before it is parsed and a non-conforming one is refused as a named error, never absorbed into a degraded result; the tightest class sits at or below the figure the gate names.",
		Measured: "that every return a real driven build actually produces is at or below that figure. The wider class — a report that its artifact is written, with the paths — is permitted a ceiling above it, so the mechanism bounds returns without enforcing the figure itself.",
		check:    returnsAreBoundedPointers,
	}, {
		ID:       "v1/every-node-runs-at-the-default-window",
		Source:   SourceV1Gates,
		Bar:      BarBuild,
		Requires: "Every node in this version runs at the default context window; no node is pinned to the wide one, which is the single largest spend lever and is taken on evidence only.",
		Asserts:  "Every stage declared in the shipped harness policy runs at the default window; no declaration anywhere in the tree pins the wide window.",
		check:    everyNodeAtDefaultWindow,
	}, {
		ID:       "v1/compaction-can-be-attributed",
		Source:   SourceV1Gates,
		Bar:      BarMechanism,
		Requires: "Every compaction is captured with the node, model, context window, tokens at compaction and gate, so the wide-window decision is later made on data.",
		Asserts:  "Every transition the engine journals carries the node, model, context window, effort and layer a compaction would be attributed by, and spend is priced out of process from the session's own transcript rather than by reading it into the driving session.",
		Measured: "the compaction record itself — including the tokens held at the moment of compaction — which the driving harness captures with its own compaction hook. That hook is harness surface, not engine surface, and this engine ships none.",
		check:    compactionCanBeAttributed,
	}, {
		ID:       "v1/governed-operations-route-through-the-engine",
		Source:   SourceV1Gates,
		Bar:      BarMechanism,
		Requires: "The share of tool calls spent on raw shell drops materially, because the operations the engine supersedes are routed to it.",
		Asserts:  "The plugin declares a routing rule for every operation the engine supersedes, each naming the invocation that replaces the raw command; the adoption gate over the frozen fixtures is part of the test suite; the routing hook fails open when the engine is unavailable.",
		Measured: "the live share of tool calls that go through the engine rather than raw shell, over a real driven build.",
		check:    governedOperationsRouted,
	}, {
		ID:       "v1/dispatch-is-deterministic",
		Source:   SourceV1Gates,
		Bar:      BarBuild,
		Requires: "Dispatches are deterministic: the same graph admits the same batch, in the same order, every time.",
		Asserts:  "Repeated admission over one graph yields identical members and identical deferrals, and admission order follows the graph's own insertion order rather than map iteration.",
		check:    dispatchIsDeterministic,
	}}
}

// orchestratorHoldsPointersOnly checks a directive names work rather than
// carrying it.
func orchestratorHoldsPointersOnly(t *Tree) []string {
	var out []string
	detail := "the acceptance criterion nobody should have to read to schedule this node"
	nodes := []dag.Node{
		fixtureNode("a", "svc/a", dag.StatusReady),
		fixtureNode("b", "svc/b", dag.StatusReady),
	}
	nodes[0].Title = detail
	st := fixtureState(nodes, dag.GatePending)
	d, err := stepOf(t, st)
	if err != nil {
		return []string{note("%v", err)}
	}
	encoded := encode(d)
	if strings.Contains(encoded, detail) {
		out = append(out, note("a directive carried a node's own prose rather than its identity"))
	}
	if d.Launch == nil {
		return append(out, note("the fixture did not reach a launch, so nothing was checked"))
	}
	if !slices.Equal(d.Launch.Members, []string{"a", "b"}) {
		out = append(out, note("a launch named %v rather than the node identities", d.Launch.Members))
	}
	// The detail record a dispatch resolves is reachable by reference only, so
	// none of the members that carry a node's own content may appear.
	for _, field := range []string{"intent", "acceptance", "stages", "prompt", "result"} {
		if strings.Contains(encoded, `"`+field+`"`) {
			out = append(out, note("a directive carries the %q of a node, which belongs in its detail record", field))
		}
	}
	slices.Sort(out)
	return out
}

// directiveSizeIsFlat checks nothing the driver receives grows with completed
// work.
func directiveSizeIsFlat(t *Tree) []string {
	var out []string
	small, err := stepOf(t, graphWithCompleted(1))
	if err != nil {
		return []string{note("%v", err)}
	}
	large, err := stepOf(t, graphWithCompleted(50))
	if err != nil {
		return []string{note("%v", err)}
	}
	if encode(small) != encode(large) {
		out = append(out, note("the same next action encodes differently at 1 and 50 completed nodes: %d bytes vs %d bytes",
			len(encode(small)), len(encode(large))))
	}
	slices.Sort(out)
	return out
}

// graphWithCompleted builds a graph holding done nodes and the same two ready
// ones, so only the volume of finished work differs.
func graphWithCompleted(done int) dag.State {
	nodes := []dag.Node{
		fixtureNode("ready-a", "svc/a", dag.StatusReady),
		fixtureNode("ready-b", "svc/b", dag.StatusReady),
	}
	for i := range done {
		nodes = append(nodes, fixtureNode(fmt.Sprintf("done-%02d", i), fmt.Sprintf("done/%02d", i), dag.StatusDone))
	}
	return fixtureState(nodes, dag.GatePending)
}

// returnsAreBoundedPointers checks the return boundary refuses rather than
// salvages.
func returnsAreBoundedPointers(t *Tree) []string {
	var out []string
	classes := []boundary.ReturnClass{boundary.ClassControlPlane, boundary.ClassDeliverable}
	tightest := -1
	for _, class := range classes {
		ceiling, err := class.Ceiling()
		if err != nil {
			out = append(out, note("the %s class declares no ceiling: %v", class, err))
			continue
		}
		if tightest < 0 || ceiling < tightest {
			tightest = ceiling
		}
		oversize := []byte(`{"status":"pass","next_action":"none","facts":["` + strings.Repeat("x", ceiling) + `"]}`)
		if _, err := boundary.Validate(class, oversize); !errors.Is(err, boundary.ErrOverLength) {
			out = append(out, note("an over-length %s return was not refused as over-length: %v", class, err))
		}
		if _, err := boundary.Validate(class, []byte(`{"status":"pass"}`)); !errors.Is(err, boundary.ErrNonConforming) {
			out = append(out, note("a %s return missing a required field was not refused as non-conforming: %v", class, err))
		}
		if _, err := boundary.Validate(class, []byte(`{"status":"pass","next_action":"none","spill":"x"}`)); !errors.Is(err, boundary.ErrNonConforming) {
			out = append(out, note("a %s return carrying an undeclared field was accepted", class))
		}
		manifest, err := boundary.Validate(class, []byte(`{"status":"pass","next_action":"none","artifact_paths":["docs/out.md"]}`))
		if err != nil || manifest.Status != "pass" {
			out = append(out, note("a conforming %s return was refused: %v", class, err))
		}
	}
	if tightest > pointerReturnGate {
		out = append(out, note("the tightest declared ceiling is %d bytes, above the %d-byte figure the gate names", tightest, pointerReturnGate))
	}
	declared := 0
	for name := range intConsts(t, "internal/dispatch/boundary/boundary.go") {
		if strings.HasSuffix(name, "CeilingBytes") {
			declared++
		}
	}
	if declared != len(classes) {
		out = append(out, note("%d ceilings are declared for %d return classes; each belongs in exactly one place", declared, len(classes)))
	}
	slices.Sort(out)
	return out
}

// everyNodeAtDefaultWindow checks nothing is pinned to the wide window.
func everyNodeAtDefaultWindow(t *Tree) []string {
	var out []string
	h, _, err := harnessOf(t)
	if err != nil {
		return []string{note("%v", err)}
	}
	stages := slices.Clone(h.Workflow.Stages)
	for _, route := range h.Routes {
		stages = append(stages, route.Stages...)
	}
	if len(stages) == 0 {
		return []string{note("%s: declares no stage, so no window was checked", examplePolicy)}
	}
	for _, s := range stages {
		if s.ContextWindow != "" && s.ContextWindow != defaultContextWindow {
			out = append(out, note("%s: stage %q runs at %q rather than the default window", examplePolicy, s.Stage, s.ContextWindow))
		}
	}
	for _, ext := range []string{".json", ".md", ".go"} {
		for _, rel := range t.Grep(ext, widePin) {
			if rel == vocabularyFile {
				continue
			}
			out = append(out, note("%s: pins the wide context window", rel))
		}
	}
	slices.Sort(out)
	return out
}

// compactionCanBeAttributed checks a journalled transition carries what a
// compaction record would be attributed by.
func compactionCanBeAttributed(t *Tree) []string {
	var out []string
	event := t.Text("schemas/anoikis/run-log-event.schema.json")
	for _, field := range []string{"node_id", "model", "context_window", "effort", "layer_seq"} {
		if !strings.Contains(event, `"`+field+`"`) {
			out = append(out, note("run-log-event.schema.json: carries no %s, so a compaction cannot be attributed to a node's run", field))
		}
	}
	source := t.Text("internal/usage/usage.go")
	if !strings.Contains(source, "type Provider interface") {
		out = append(out, note("internal/usage/usage.go: spend is not behind a provider interface, so it cannot be priced out of process"))
	}
	if !strings.Contains(source, "type Unavailable struct") {
		out = append(out, note("internal/usage/usage.go: there is no provider for a harness that wires no spend source, so unmeasured spend has nowhere honest to go"))
	}
	slices.Sort(out)
	return out
}

// routingRules is the plugin's declaration of which operations route to the
// engine.
const routingRules = "plugin/routing-rules.json"

// adoptionGate is the test that scores those rules against frozen fixtures.
const adoptionGate = "internal/plugincheck/adoption_test.go"

// routingHook is the plugin's own hook; sharedRoutingHook is the shared
// implementation it hands over to, which owns the deny-or-allow decision.
const (
	routingHook       = "plugin/hooks/pretooluse-forced-use.sh"
	sharedRoutingHook = "plugin/hooks/forced-use-hook.sh"
)

// shellTool is the tool a raw command is run through, and the only kind of
// operation a command prefix can recognise.
const shellTool = "Bash"

// governedOperationsRouted checks the raw commands the engine supersedes are
// routed to it.
func governedOperationsRouted(t *Tree) []string {
	var out []string
	var rules struct {
		Operations []struct {
			Name string `json:"name"`
			CLI  struct {
				InvocationPrefix string `json:"invocation_prefix"`
				UsageHint        string `json:"usage_hint"`
			} `json:"cli"`
			Raw struct {
				ToolName        string   `json:"tool_name"`
				CommandPrefixes []string `json:"command_prefixes"`
			} `json:"raw"`
		} `json:"operations"`
	}
	if err := t.JSON(routingRules, &rules); err != nil {
		return []string{note("%v", err)}
	}
	if len(rules.Operations) == 0 {
		return []string{note("%s: declares no governed operation", routingRules)}
	}
	for _, op := range rules.Operations {
		switch {
		case op.Name == "":
			out = append(out, note("%s: an operation has no name", routingRules))
		case op.CLI.InvocationPrefix == "":
			out = append(out, note("%s: operation %q names no invocation to route to", routingRules, op.Name))
		case op.CLI.UsageHint == "":
			out = append(out, note("%s: operation %q offers no usage hint, so a redirect cannot say what to run", routingRules, op.Name))
		case op.Raw.ToolName == "":
			out = append(out, note("%s: operation %q names no raw tool it supersedes", routingRules, op.Name))
		case op.Raw.ToolName == shellTool && len(op.Raw.CommandPrefixes) == 0:
			// Only a shell operation is recognised by its command; a tool that
			// takes no command is superseded whole.
			out = append(out, note("%s: shell operation %q names no command it supersedes", routingRules, op.Name))
		}
	}
	if !t.Has(adoptionGate) {
		out = append(out, note("%s: the adoption gate over the frozen fixtures is missing", adoptionGate))
	}
	// The plugin's own hook only supplies paths and hands over to the shared
	// implementation, which is where failing open is decided, so both are
	// checked: the wrapper delegates, and what it delegates to lets the tool
	// through rather than denying it.
	wrapper := t.Text(routingHook)
	switch {
	case wrapper == "":
		out = append(out, note("%s: the routing hook is missing", routingHook))
	case !strings.Contains(wrapper, path.Base(sharedRoutingHook)):
		out = append(out, note("%s: does not hand over to the shared routing hook, so failing open is unowned", routingHook))
	}
	shared := t.Text(sharedRoutingHook)
	switch {
	case shared == "":
		out = append(out, note("%s: the shared routing hook is missing", sharedRoutingHook))
	case !strings.Contains(shared, "exit 0"):
		out = append(out, note("%s: has no path that lets the tool through, so it cannot fail open", sharedRoutingHook))
	}
	slices.Sort(out)
	return out
}

// subsequence reports whether members appear in whole, in order, within order.
func subsequence(members, order []string) bool {
	next := 0
	for _, id := range order {
		if next < len(members) && members[next] == id {
			next++
		}
	}
	return next == len(members)
}

// dispatchIsDeterministic checks admission is a pure function of the graph.
func dispatchIsDeterministic(t *Tree) []string {
	var out []string
	h, prover, err := harnessOf(t)
	if err != nil {
		return []string{note("%v", err)}
	}
	st := fixtureState([]dag.Node{
		fixtureNode("a", "svc/a", dag.StatusReady),
		fixtureNode("b", "svc/b", dag.StatusReady),
		fixtureNode("c", "svc/a", dag.StatusReady),
		fixtureNode("d", "svc/d", dag.StatusReady),
	}, dag.GatePending)
	g, err := st.Graph(ids.Opaque{})
	if err != nil {
		return []string{note("%v", err)}
	}
	var first admission.Batch
	for i := range 5 {
		batch, err := admission.Admit(g, st.Ready(g), prover, h.MaxGroupSize())
		if err != nil {
			return append(out, note("admit: %v", err))
		}
		if i == 0 {
			first = batch
			continue
		}
		if !slices.Equal(batch.Members, first.Members) {
			out = append(out, note("admission yielded %v then %v over one graph", first.Members, batch.Members))
		}
		if len(batch.Deferred) != len(first.Deferred) {
			out = append(out, note("admission deferred %d nodes then %d over one graph", len(first.Deferred), len(batch.Deferred)))
		}
	}
	if !subsequence(first.Members, st.Ready(g)) {
		out = append(out, note("admission returned %v, which is not in the graph's own order %v", first.Members, st.Ready(g)))
	}
	slices.Sort(out)
	return slices.Compact(out)
}
