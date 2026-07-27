package engine

import (
	"fmt"

	"github.com/johnrichter/anoikis-tools/internal/dag"
)

// ResumeAction is what a resume does about one previously dispatched run.
type ResumeAction string

// The closed resume-action set.
const (
	// ResumeSkip is a run already merged: nothing to do.
	ResumeSkip ResumeAction = "skip"
	// ResumeReissue is a run dispatched but never finished — the process
	// died mid-run. Its worktree is reset to the commit it branched from and
	// the stored prompt is replayed verbatim.
	ResumeReissue ResumeAction = "reissue"
	// ResumeRerecord is a run that finished but was never merged — the
	// process died between the run returning and the merge. Recording is
	// idempotent, so it simply runs again.
	ResumeRerecord ResumeAction = "rerecord"
)

// ResumeItem is one run's disposition.
type ResumeItem struct {
	RunID       string       `json:"run_id"`
	NodeID      string       `json:"node_id"`
	Action      ResumeAction `json:"action"`
	Reason      string       `json:"reason"`
	BaseRef     string       `json:"base_ref,omitempty"`
	WorktreeRef string       `json:"worktree_ref,omitempty"`
	PromptRef   string       `json:"prompt_ref,omitempty"`
	// PromptDigest lets a replay prove it is reissuing the same bytes that
	// were dispatched.
	PromptDigest string `json:"prompt_digest,omitempty"`
}

// ResumePlan is what a fresh session must do to pick a killed build back up.
type ResumePlan struct {
	Items []ResumeItem `json:"items"`
	// Damaged counts run-log lines that could not be read. A process killed
	// mid-append leaves at most one, at the end of the file.
	Damaged int `json:"damaged,omitempty"`
	// DamageDetail describes the damage, so it is surfaced as a caveat
	// rather than passing unnoticed.
	DamageDetail string    `json:"damage_detail,omitempty"`
	Commands     []Command `json:"commands,omitempty"`
}

// Reissued returns the items that must be launched again.
func (p ResumePlan) Reissued() []ResumeItem { return p.filter(ResumeReissue) }

// Rerecorded returns the items whose recording must run again.
func (p ResumePlan) Rerecorded() []ResumeItem { return p.filter(ResumeRerecord) }

func (p ResumePlan) filter(a ResumeAction) []ResumeItem {
	var out []ResumeItem
	for _, it := range p.Items {
		if it.Action == a {
			out = append(out, it)
		}
	}
	return out
}

// Resume decides how to pick a killed build back up, from the run log alone.
//
// Each run is classified by its latest recorded event, because the log is
// append-only and never rewritten: merged is finished, complete or failed
// means the work ran but was not settled, and dispatched with nothing after
// it means the process died mid-run. A run whose dispatch was never
// journalled does not appear here at all and is simply scheduled again by the
// next Step — which is exactly right, since nothing was launched.
//
// damaged and damageDetail describe run-log lines that could not be read.
// They are carried through as a caveat rather than an error: the events
// before the damage are complete, and the transition the damaged line would
// have described is treated as never having happened. A hard kill costs the
// work in flight and never the record of what came before it.
func Resume(st dag.State, damaged int, damageDetail string, env Env) ResumePlan {
	plan := ResumePlan{Damaged: damaged, DamageDetail: damageDetail}
	for _, e := range st.LatestEvents() {
		item := ResumeItem{
			RunID:        e.RunID,
			NodeID:       e.NodeID,
			BaseRef:      e.BaseRef,
			WorktreeRef:  e.WorktreeRef,
			PromptRef:    e.PromptRef,
			PromptDigest: e.PromptDigest,
		}
		switch e.Event {
		case dag.EventMerged, dag.EventGrafted:
			item.Action, item.Reason = ResumeSkip, "already settled"
		case dag.EventComplete, dag.EventFailed:
			item.Action = ResumeRerecord
			item.Reason = "the run finished but was never merged; recording is idempotent and completes it"
		case dag.EventDispatched:
			item.Action = ResumeReissue
			item.Reason = "the run was dispatched and never finished; reset its worktree and replay the stored prompt verbatim"
		default:
			item.Action, item.Reason = ResumeSkip, fmt.Sprintf("unrecognised event %q", e.Event)
		}
		plan.Items = append(plan.Items, item)
	}
	if len(plan.Rerecorded()) > 0 {
		plan.Commands = append(plan.Commands, Command{
			Purpose: "settle the runs that finished but were never merged",
			Argv:    []string{env.Tool, "record", "--effort", env.Effort, "--results", "<results-file>"},
		})
	}
	if len(plan.Reissued()) > 0 {
		plan.Commands = append(plan.Commands, Command{
			Purpose: "reset each interrupted worktree to its base commit and replay its stored prompt verbatim",
			Argv:    []string{env.Tool, "reissue", "--effort", env.Effort},
		})
	}
	return plan
}
