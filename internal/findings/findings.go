// Package findings is the effort's ranked feedback register: what the build
// noticed but did not act on, kept where the next planning cycle will read it.
//
// It is a thin composition over the shared ranked-register library, which
// already derives an entry's id and criticality itself — neither is something
// a caller or a model can supply — and writes the canonical document and its
// Markdown mirror as one atomic pair. What this package adds is the two
// engine-facing questions: which findings block the build right now, and
// which ones fold into carryover when a version closes.
package findings

import (
	"fmt"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/claude-shared-tooling/go/ledger"
)

// Register is one effort's findings backlog.
type Register struct {
	l         *ledger.Ledger
	threshold int
}

// Open loads the register for an effort. threshold is the criticality at or
// above which a finding blocks the build; it is a policy input, never a
// constant here.
func Open(l effort.Layout, threshold int) (*Register, error) {
	lg, err := ledger.Open(l.Findings(), l.FindingsMirror(), effort.FilePerm)
	if err != nil {
		return nil, fmt.Errorf("findings: open register: %w", err)
	}
	if threshold < 1 {
		return nil, fmt.Errorf("findings: blocking threshold must be at least 1, got %d", threshold)
	}
	return &Register{l: lg, threshold: threshold}, nil
}

// Add records a finding. Its id and criticality are derived by the register,
// so two runs raising the same observation still produce comparable, ranked
// entries.
func (r *Register) Add(seed dag.FindingSeed) (ledger.Entry, error) {
	e, err := r.l.Add(seed.Statement, seed.Impact, seed.Urgency)
	if err != nil {
		return ledger.Entry{}, fmt.Errorf("findings: add: %w", err)
	}
	return e, nil
}

// AddAll records every finding a run raised and returns the ids they resolve
// to.
//
// A seed whose statement already sits unresolved in the register matches that
// entry instead of adding a second copy, so replaying a batch's outcomes after
// an interrupted merge leaves the register exactly as one clean run would.
func (r *Register) AddAll(seeds []dag.FindingSeed) ([]string, error) {
	var out []string
	for _, s := range seeds {
		if existing, ok := r.unresolved(s.Statement); ok {
			out = append(out, existing.ID)
			continue
		}
		e, err := r.Add(s)
		if err != nil {
			return out, err
		}
		out = append(out, e.ID)
	}
	return out, nil
}

// unresolved returns the open entry carrying exactly this statement.
func (r *Register) unresolved(statement string) (ledger.Entry, bool) {
	for _, e := range r.l.List() {
		if e.Statement == statement && !e.Resolution.Known() {
			return e, true
		}
	}
	return ledger.Entry{}, false
}

// Blocking returns the unresolved findings whose criticality reaches the
// threshold. One of these halts the build: the observation is severe enough
// that continuing would build on top of it.
func (r *Register) Blocking() []ledger.Entry {
	actNow, _ := r.l.Partition(r.threshold)
	var out []ledger.Entry
	for _, e := range actNow {
		if !e.Resolution.Known() {
			out = append(out, e)
		}
	}
	return out
}

// Deferred returns the unresolved findings below the threshold — the backlog
// that folds into carryover at version close.
func (r *Register) Deferred() []ledger.Entry {
	_, deferred := r.l.Partition(r.threshold)
	var out []ledger.Entry
	for _, e := range deferred {
		if !e.Resolution.Known() {
			out = append(out, e)
		}
	}
	return out
}

// List returns every entry, criticality-ranked.
func (r *Register) List() []ledger.Entry { return r.l.List() }

// Fold closes out a version: every deferred finding is resolved as carried,
// cited by the version it was carried out of, and returned as the carryover
// lines the manifest keeps. Carryover is the sole memory that crosses a
// version boundary, so this is the one place it is written.
func (r *Register) Fold(version string) ([]string, []string, error) {
	if version == "" {
		return nil, nil, fmt.Errorf("findings: a version is required to fold against")
	}
	citation := ledger.Citation{Kind: ledger.CitationReleaseTag, Value: version}
	var lines, folded []string
	for _, e := range r.Deferred() {
		if _, err := r.l.Resolve(e.ID, ledger.ResolutionCarried, citation); err != nil {
			return nil, nil, fmt.Errorf("findings: carry %s: %w", e.ID, err)
		}
		lines = append(lines, fmt.Sprintf("%s (criticality %d, carried from %s): %s", e.ID, e.Criticality, version, e.Statement))
		folded = append(folded, e.ID)
	}
	return lines, folded, nil
}
