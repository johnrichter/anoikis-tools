package engine

import (
	"fmt"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/policy"
)

// StageDispatch is one stage of one node's run, resolved to the agent that
// runs it and the tier it runs at.
type StageDispatch struct {
	Stage         string `json:"stage"`
	Role          string `json:"role"`
	Agent         string `json:"agent"`
	Model         string `json:"model"`
	ContextWindow string `json:"context_window,omitempty"`
	Effort        string `json:"effort,omitempty"`
}

// Dispatch is everything needed to launch one node: the run's identity, the
// worktree it owns for the whole run, the stages to run in it, and the
// verbatim prompt bytes the run is issued with.
type Dispatch struct {
	NodeID string `json:"node_id"`
	RunID  string `json:"run_id"`
	// BaseRef is the commit the node's worktree branches from; a hard-kill
	// resume hard-resets to it before replaying.
	BaseRef string `json:"base_ref"`
	// WorktreeRef is the branch checked out in the node's own worktree — one
	// per node, shared across its stages.
	WorktreeRef string          `json:"worktree_ref"`
	Stages      []StageDispatch `json:"stages"`
	// ReviewRole is set for a node whose verification tier earns a dedicated
	// review at node close rather than waiting for the gate.
	ReviewRole string `json:"review_role,omitempty"`
	// Prompt is the rendered dispatch text. It is written to disk before the
	// run is journalled, and a resume replays these exact bytes.
	Prompt string `json:"prompt"`
}

// PlanDispatch resolves the dispatches for one admitted batch.
//
// Every stage's role, model and tier come from the route the node's
// deliverable kind selects, falling back to the harness's default workflow —
// there is no path by which an unrouted kind reaches a builder. The result is
// a pure function of the state and policy passed in, so the same layer plans
// identically on every machine.
func PlanDispatch(st dag.State, h *policy.Harness, details map[string]dag.Detail, members []string, layerSeq int, env Env) ([]Dispatch, error) {
	out := make([]Dispatch, 0, len(members))
	for _, id := range members {
		node, ok := st.Node(id)
		if !ok {
			return nil, fmt.Errorf("engine: batch names unknown node %s", id)
		}
		if node.NeverDispatch {
			return nil, fmt.Errorf("engine: node %s is marked never-dispatch and must not be handed to an agent", id)
		}
		detail, ok := details[id]
		if !ok {
			return nil, fmt.Errorf("engine: no detail record loaded for node %s", id)
		}
		stages, err := resolveStages(h, detail)
		if err != nil {
			return nil, err
		}
		d := Dispatch{
			NodeID:      id,
			RunID:       RunID(id, layerSeq, node.Attempts),
			BaseRef:     env.BaseRef,
			WorktreeRef: WorktreeBranch(env.Effort, id, layerSeq),
			Stages:      stages,
		}
		if node.VerifyTier == dag.VerifyImmediateDeep {
			d.ReviewRole = h.ReviewRoleFor(detail.DeliverableKind)
		}
		d.Prompt = RenderPrompt(h, node, detail, d)
		out = append(out, d)
	}
	return out, nil
}

// resolveStages resolves a node's stages against the route its deliverable
// kind selects, filling each stage's tier from the node's own declaration
// where it has one and from the route otherwise.
func resolveStages(h *policy.Harness, detail dag.Detail) ([]StageDispatch, error) {
	routed, err := h.StagesFor(detail.DeliverableKind)
	if err != nil {
		return nil, err
	}
	declared := map[string]dag.Stage{}
	for _, s := range detail.Stages {
		declared[s.Stage] = s
	}
	out := make([]StageDispatch, 0, len(routed))
	for _, s := range routed {
		role, ok := h.Roles[s.Role]
		if !ok {
			return nil, fmt.Errorf("engine: route for %s names undeclared role %q", detail.DeliverableKind, s.Role)
		}
		sd := StageDispatch{
			Stage:         s.Stage,
			Role:          s.Role,
			Agent:         role.Agent,
			Model:         s.Model,
			ContextWindow: s.ContextWindow,
			Effort:        s.Effort,
		}
		if d, ok := declared[s.Stage]; ok {
			sd.Model = firstNonEmpty(d.Model, sd.Model)
			sd.ContextWindow = firstNonEmpty(d.ContextWindow, sd.ContextWindow)
			sd.Effort = firstNonEmpty(d.Effort, sd.Effort)
		}
		if sd.Model == "" {
			return nil, fmt.Errorf("engine: node %s stage %q declares no model and its route supplies none", detail.ID, s.Stage)
		}
		out = append(out, sd)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("engine: node %s resolved to no stages", detail.ID)
	}
	return out, nil
}

// RunID names one attempt at one node in one layer. It is derived, not
// generated, so replaying a killed run reissues under the same identity and
// the run log's last-event-per-run reduction stays correct.
func RunID(nodeID string, layerSeq, attempt int) string {
	return fmt.Sprintf("%s-l%d-a%d", sanitize(nodeID), layerSeq, attempt)
}

// WorktreeBranch names the branch a node's own worktree checks out. One
// worktree per node, shared across its stages, keeps every node's output
// path-disjoint by construction.
func WorktreeBranch(effort, nodeID string, layerSeq int) string {
	return fmt.Sprintf("%s/%s-l%d", sanitize(effort), sanitize(nodeID), layerSeq)
}

// RenderPrompt builds a run's dispatch text.
//
// The text is assembled from the role's own brief plus the node's declared
// intent, acceptance criteria, resource surface and inputs. It states the
// two-channel return contract every dispatch is held to — deliverable to a
// file, a bounded manifest as the message — because a run that returns its
// deliverable in the message is paid for twice and rejected once.
func RenderPrompt(h *policy.Harness, node dag.Node, detail dag.Detail, d Dispatch) string {
	var b strings.Builder
	role := ""
	if len(d.Stages) > 0 {
		role = d.Stages[0].Role
	}
	if r, ok := h.Roles[role]; ok {
		b.WriteString(r.Brief)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Node: %s — %s\n", node.ID, node.Title)
	fmt.Fprintf(&b, "Deliverable kind: %s\n", detail.DeliverableKind)
	fmt.Fprintf(&b, "Worktree branch: %s (branched from %s)\n", d.WorktreeRef, d.BaseRef)
	fmt.Fprintf(&b, "\nIntent\n%s\n", detail.Intent)

	b.WriteString("\nAcceptance\n")
	for _, a := range detail.Acceptance {
		fmt.Fprintf(&b, "- %s\n", a)
	}

	b.WriteString("\nDeclared resource surface — stay inside it; changes outside it are re-asserted against this list after the merge\n")
	if len(node.Surface) == 0 {
		b.WriteString("- (none declared)\n")
	}
	for _, c := range node.Surface {
		kind := c.Kind
		if kind == "" {
			kind = "unspecified"
		}
		fmt.Fprintf(&b, "- %s/%s: %s\n", c.Domain, kind, c.Value)
	}

	if detail.Inputs != nil && len(detail.Inputs.SpecRefs) > 0 {
		b.WriteString("\nInputs\n")
		for _, s := range detail.Inputs.SpecRefs {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}

	b.WriteString("\nStages, in order\n")
	for _, s := range d.Stages {
		fmt.Fprintf(&b, "- %s: %s (%s", s.Stage, s.Role, s.Model)
		if s.Effort != "" {
			fmt.Fprintf(&b, "/%s", s.Effort)
		}
		if s.ContextWindow != "" {
			fmt.Fprintf(&b, ", %s context", s.ContextWindow)
		}
		b.WriteString(")\n")
	}

	b.WriteString("\nReturn contract\n")
	b.WriteString("- Write the deliverable to a file inside this node's worktree. Do not return its content.\n")
	b.WriteString("- Return a bounded manifest: status, artifact paths, a few key facts, the next action.\n")
	b.WriteString("- A control-plane verdict is itself a manifest and does return as the message.\n")
	return b.String()
}

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// sanitize renders an id as a ref-safe component: anything outside the
// portable set becomes a hyphen.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}
