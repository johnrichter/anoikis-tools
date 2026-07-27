package vcs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/claude-shared-tooling/go/sysops"
)

// backstopExcerpt bounds how much of a failed backstop's output is carried
// forward, so a wall of compiler noise never becomes the report.
const backstopExcerpt = 4000

// BackstopResult is what running the post-merge check found.
type BackstopResult struct {
	// Command is the argv that ran, so a report names exactly what was
	// checked.
	Command []string `json:"command"`
	// ExitCode is the check's exit status.
	ExitCode int `json:"exit_code"`
	// Passed is true only on a zero exit.
	Passed bool `json:"passed"`
	// Excerpt is the tail of the check's output when it failed.
	Excerpt string `json:"excerpt,omitempty"`
	// DurationMS is how long the check took.
	DurationMS int64 `json:"duration_ms"`
}

// Backstop runs the post-merge check over a merged layer.
//
// It always runs. A disjointness proof works over declared text and cannot
// see two nodes adding the same new symbol to one package, a path aliased by
// a symlink, or a node that wrote outside what it declared — every one of
// those produces a text-clean merge that does not build. Building the merged
// result is the only thing that catches them, so it is not a policy choice:
// the harness supplies the command, and a harness with no command is refused
// when its policy loads.
func (r *Repo) Backstop(ctx context.Context, command []string, timeout time.Duration) (BackstopResult, error) {
	if len(command) == 0 {
		return BackstopResult{}, fmt.Errorf("vcs: the post-merge backstop has no command; it is always on and cannot be skipped")
	}
	res, err := sysops.Run(ctx, command[0], command[1:], sysops.Options{Dir: r.dir, Timeout: timeout})
	if err != nil {
		return BackstopResult{Command: command, ExitCode: -1}, fmt.Errorf("vcs: run backstop %s: %w", strings.Join(command, " "), err)
	}
	out := BackstopResult{
		Command:    command,
		ExitCode:   res.ExitCode,
		Passed:     res.ExitCode == 0,
		DurationMS: res.Duration.Milliseconds(),
	}
	if !out.Passed {
		out.Excerpt = tail(string(res.Stdout)+string(res.Stderr), backstopExcerpt)
	}
	return out, nil
}

// AssertSurfaces re-asserts, against what the merge actually landed, that
// every changed path was declared by one of the merged nodes, and returns the
// paths none of them declared.
//
// This is the reverse of the check admission makes. Admission asks whether
// declared surfaces are disjoint; this asks whether the declarations were
// true. A node that quietly writes outside its surface is invisible to every
// proof made before it ran, and the artifact only drops at the merge — which
// is why the assertion belongs here and nowhere earlier.
//
// Only path-domain claims are checkable this way: a namespace claim has no
// counterpart in a diff, so claims in other domains neither cover nor
// implicate a path.
func AssertSurfaces(changed []string, nodes map[string][]dag.Claim, pathDomains []string) []string {
	domains := map[string]bool{}
	for _, d := range pathDomains {
		domains[d] = true
	}
	var drift []string
	for _, path := range changed {
		if path == "" {
			continue
		}
		if !covered(path, nodes, domains) {
			drift = append(drift, path)
		}
	}
	return drift
}

// covered reports whether any node declared a path claim matching path.
func covered(path string, nodes map[string][]dag.Claim, domains map[string]bool) bool {
	for _, claims := range nodes {
		for _, c := range claims {
			if !domains[c.Domain] {
				continue
			}
			if matches(c, path) {
				return true
			}
		}
	}
	return false
}

// matches reports whether one path claim covers path, in the same glob
// dialect admission proved disjointness with — a laxer or stricter dialect
// here would make the two checks disagree about the same declaration.
//
// A claim whose kind the path domain does not decide covers nothing, exactly
// as it proves nothing. The readiness gate refuses such a claim up front, so
// reaching it here means a surface changed after validation.
func matches(c dag.Claim, path string) bool {
	value := strings.TrimPrefix(strings.TrimSpace(c.Value), "./")
	switch c.Kind {
	case "dir":
		return path == value || strings.HasPrefix(path, strings.TrimSuffix(value, "/")+"/")
	case "file":
		return path == value
	case "glob":
		ok, err := doublestar.Match(value, path)
		return err == nil && ok
	default:
		return false
	}
}

// tail returns at most n bytes from the end of s, marking a truncation.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "...(truncated)...\n" + s[len(s)-n:]
}
