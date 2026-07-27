// Package usage is the spend seam.
//
// What a run cost is never self-reported by the agent that ran it — an agent
// cannot measure its own billed tokens — so the engine asks a Provider. The
// interface is the whole coupling: swapping where spend comes from, or
// running with no spend source at all, changes which Provider is injected and
// nothing else.
//
// The one rule every implementation shares is that unmeasured spend is
// reported as unknown, never as zero. A zero that means "no data" is
// indistinguishable from a zero that means "free", and once summed it silently
// understates a budget.
package usage

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/claude-shared-tooling/go/cost"
	"github.com/johnrichter/claude-shared-tooling/go/transcript"
)

// Run identifies one dispatched run to a Provider. Every field is a fact the
// engine already holds from dispatching it — nothing here is discovered by
// scanning a directory or comparing modification times, because a concurrent
// sibling run would make either unsafe.
type Run struct {
	// Project labels the effort the run belongs to.
	Project string
	// SessionID is the session the run was dispatched from.
	SessionID string
	// Agent names the agent that ran, empty for the orchestrating session
	// itself.
	Agent string
	// TranscriptPath, when set, is used verbatim instead of resolving one
	// from SessionID.
	TranscriptPath string
}

// Provider reports what a run cost.
type Provider interface {
	// Name identifies the provider in reports and in an unknown-usage
	// reason, so a reader can tell "no provider wired" from "provider had no
	// data".
	Name() string

	// RunUsage returns r's spend. A run the provider cannot price yields a
	// Usage with Known false and a Reason, never an error and never a zero
	// that reads as free.
	RunUsage(ctx context.Context, r Run) (dag.Usage, error)

	// Close releases whatever the provider holds open.
	Close() error
}

// Unavailable is the Provider used when a harness wires no spend source. It
// answers every query with an explicit unknown carrying the reason, which is
// what keeps a budget honest about what it has not measured.
type Unavailable struct {
	// Reason states why no spend source is available.
	Reason string
}

// Name identifies the provider.
func (Unavailable) Name() string { return "unavailable" }

// RunUsage returns an explicit unknown.
func (u Unavailable) RunUsage(context.Context, Run) (dag.Usage, error) {
	reason := u.Reason
	if reason == "" {
		reason = "no usage provider is configured for this harness"
	}
	return dag.Usage{Known: false, Reason: reason}, nil
}

// Close is a no-op.
func (Unavailable) Close() error { return nil }

// Transcripts prices runs from the session transcripts a coding agent leaves
// behind, through the format-agnostic transcript seam and the shared cost
// index.
//
// It resolves a transcript deterministically from the session id it was given
// — never by picking the newest file in a directory, which concurrent runs
// make unsafe — ingests it resumably, and sums the stored per-turn events for
// the agent in question. Because ingest is watermarked, asking twice about a
// transcript still being written adds only the turns that arrived in between.
type Transcripts struct {
	source transcript.TranscriptSource
	store  *cost.Store
	root   string
	scope  string
}

// OpenTranscripts opens a transcript-backed provider. root and scope locate
// the coding agent's transcript storage; indexPath is where the cost index is
// kept.
func OpenTranscripts(source transcript.TranscriptSource, root, scope, indexPath string) (*Transcripts, error) {
	if source == nil {
		return nil, fmt.Errorf("usage: a transcript source is required")
	}
	if root == "" || indexPath == "" {
		return nil, fmt.Errorf("usage: transcript_root and index_path are both required")
	}
	store, err := cost.Open(indexPath)
	if err != nil {
		return nil, fmt.Errorf("usage: open cost index %s: %w", indexPath, err)
	}
	return &Transcripts{source: source, store: store, root: root, scope: scope}, nil
}

// Name identifies the provider.
func (*Transcripts) Name() string { return "transcripts" }

// RunUsage ingests r's transcript and returns the spend attributed to it.
//
// An unreadable transcript, an unpriceable model, or a run with no billable
// turns all resolve to an explicit unknown naming the cause. None of them
// resolve to zero.
func (t *Transcripts) RunUsage(_ context.Context, r Run) (dag.Usage, error) {
	if r.Project == "" {
		return dag.Usage{}, fmt.Errorf("usage: a project label is required")
	}
	path := r.TranscriptPath
	if path == "" {
		if r.SessionID == "" {
			return dag.Usage{Known: false, Reason: "run carries neither a transcript path nor a session id"}, nil
		}
		path = t.source.ResolvePath(t.root, t.scope, r.SessionID)
	}
	meta := cost.TranscriptMeta{Project: r.Project, IsMain: r.Agent == "", Agent: r.Agent}
	if _, err := t.store.Ingest(t.source, path, meta); err != nil {
		return dag.Usage{Known: false, Reason: fmt.Sprintf("cost index could not read %s: %v", filepath.Base(path), err)}, nil
	}
	filter := cost.QueryFilter{Project: r.Project, TranscriptPath: path}
	if r.Agent != "" {
		filter.Agent = r.Agent
	}
	events, err := t.store.Query(filter)
	if err != nil {
		return dag.Usage{Known: false, Reason: fmt.Sprintf("cost index query failed: %v", err)}, nil
	}
	if len(events) == 0 {
		return dag.Usage{Known: false, Reason: "no billable turns recorded for this run"}, nil
	}
	return total(events), nil
}

// Close releases the cost index.
func (t *Transcripts) Close() error { return t.store.Close() }

// total sums stored cost events into one usage record.
func total(events []cost.CostEvent) dag.Usage {
	u := dag.Usage{Known: true}
	var money cost.Money
	for _, e := range events {
		money += e.Total
		u.InputTokens += e.Tokens.Input
		u.CacheCreationTokens += e.Tokens.CacheWrite
		u.CacheReadTokens += e.Tokens.CacheRead
		u.OutputTokens += e.Tokens.Output
	}
	u.CostUSD = money.USD()
	return u
}
