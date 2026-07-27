// Package vcs performs the two merges a build makes, and nothing else
// touches git.
//
// The two are genuinely different operations, and keeping them apart in code
// is what makes the signing rule structural rather than advisory:
//
//   - A layer merge octopus-merges a completed batch's worktrees onto the
//     build branch. It is autonomous and unsigned, it runs after every batch,
//     and it is immediately followed by the backstop — building the merged
//     result and re-asserting what each node actually touched — because that
//     is the only place the undecidable residual of parallel work shows up.
//   - A gate merge moves the build branch onto a target. Only the harness's
//     declared main branch triggers re-signing every commit, signing the merge
//     commit, and requiring an operator-approved message. Every other target
//     merges autonomously and unsigned.
//
// The main branch therefore only ever receives a reviewed, fully signed merge
// — by construction here, not by a rule someone has to remember to read.
package vcs

import (
	"context"
	"fmt"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/claude-shared-tooling/go/git"
	"github.com/johnrichter/claude-shared-tooling/go/sysops"
)

// Repo is one repository the engine merges in.
type Repo struct {
	repo *git.Repo
	dir  string
}

// Open opens the repository containing dir.
func Open(ctx context.Context, dir string) (*Repo, error) {
	r, err := git.Open(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("vcs: open %s: %w", dir, err)
	}
	return &Repo{repo: r, dir: dir}, nil
}

// Dir is the working directory merges run in.
func (r *Repo) Dir() string { return r.dir }

// WrongBranchError reports a merge attempted from a checkout that is not the
// branch the merge was planned against. It fails closed: merging into
// whatever happens to be checked out would silently rewrite the wrong branch.
type WrongBranchError struct {
	Want, Got string
}

func (e *WrongBranchError) Error() string {
	return fmt.Sprintf("vcs: %s must be checked out to merge into it, but %s is", e.Want, e.Got)
}

// PreconditionError reports a main-branch merge attempted without something
// only an operator can supply. It is deliberately its own type: the merge was
// never started, so the state is exactly as it was, and the caller needs to
// know what to bring rather than that something went wrong.
type PreconditionError struct {
	Target  string
	Missing string
}

func (e *PreconditionError) Error() string {
	return fmt.Sprintf("vcs: merging onto %s requires %s", e.Target, e.Missing)
}

// LayerPlan is one completed batch's merge onto the build branch.
type LayerPlan struct {
	// BuildBranch must be the branch checked out in the repository.
	BuildBranch string
	// Branches are the node worktree branches to octopus-merge. Git's own
	// octopus handles one or many with no special casing here.
	Branches []string
	// Message is the merge commit's message.
	Message string
}

// LayerResult is what a layer merge produced.
type LayerResult struct {
	// Head is the build branch's new head.
	Head string
	// Base is the head the merge started from, and the range the surface
	// re-assertion and the backstop are evaluated over.
	Base string
	// Changed is every path the merge brought onto the build branch.
	Changed []string
}

// MergeLayer octopus-merges a completed batch onto the build branch.
//
// It never signs and never pauses: below the main branch, merges are
// autonomous by policy. The caller runs the backstop over the returned range
// immediately afterwards — a text-clean merge of two provably disjoint
// surfaces can still fail to build, and that is precisely the case the
// disjointness proof cannot see.
func (r *Repo) MergeLayer(ctx context.Context, plan LayerPlan) (*LayerResult, error) {
	if len(plan.Branches) == 0 {
		return nil, fmt.Errorf("vcs: a layer merge needs at least one branch")
	}
	if err := r.requireBranch(ctx, plan.BuildBranch); err != nil {
		return nil, err
	}
	base, err := r.head(ctx)
	if err != nil {
		return nil, err
	}
	message := plan.Message
	if message == "" {
		message = fmt.Sprintf("Merge layer: %s", strings.Join(plan.Branches, ", "))
	}
	res, err := r.repo.Merge(ctx, plan.Branches, git.MergeOptions{Message: message, FastForward: git.FastForwardNever})
	if err != nil {
		return nil, fmt.Errorf("vcs: octopus-merge %s onto %s: %w", strings.Join(plan.Branches, ", "), plan.BuildBranch, err)
	}
	changed, err := r.changedPaths(ctx, base, res.NewHead)
	if err != nil {
		return nil, err
	}
	return &LayerResult{Head: res.NewHead, Base: base, Changed: changed}, nil
}

// GatePlan is a gate's merge onto its target.
type GatePlan struct {
	// BuildBranch is the branch being merged onto the target.
	BuildBranch string
	// Target is the branch to merge onto.
	Target string
	// TargetsMain marks this as the one merge that re-signs and signs.
	TargetsMain bool
	// Message is the merge commit's message. Required when TargetsMain,
	// because an operator approved those exact words.
	Message string
	// ResignBase bounds the re-signing range: every commit after it, up to
	// the build branch's head, is rewritten signed. Required when
	// TargetsMain, since guessing the range would rewrite history nobody
	// asked to rewrite.
	ResignBase string
	// SignArgs are the commit-tree flags that produce a signed commit,
	// defaulting to the repository's configured key.
	SignArgs []string
}

// GateResult is what a gate merge produced.
type GateResult struct {
	// Head is the target's new head.
	Head string
	// ResignedHead is the build branch's head after re-signing, empty when
	// the merge did not re-sign.
	ResignedHead string
	// Signed reports whether the merge commit itself was signed.
	Signed bool
}

// MergeGate moves the build branch onto a gate's target.
//
// The target is checked out here and the previous branch restored afterwards,
// so a driver never composes a git command of its own and a build resumes on
// the branch it was running from. A working tree with uncommitted tracked
// changes is refused up front: those changes would not travel with the merge,
// and the effort's own state is among them.
//
// Targeting the harness's main branch is the only path that re-signs every
// commit on the build branch and signs the merge commit, and it refuses to
// run without both an operator-approved message and an explicit re-signing
// range. Any other target merges autonomously and unsigned.
func (r *Repo) MergeGate(ctx context.Context, plan GatePlan) (*GateResult, error) {
	if plan.Target == "" || plan.Target == dag.MergeTargetNone {
		return nil, fmt.Errorf("vcs: this gate declares no merge target")
	}
	if plan.TargetsMain {
		if strings.TrimSpace(plan.Message) == "" {
			return nil, &PreconditionError{Target: plan.Target, Missing: "an operator-approved merge message"}
		}
		if plan.ResignBase == "" {
			return nil, &PreconditionError{Target: plan.Target, Missing: "an explicit re-signing base; nothing here guesses which history to rewrite"}
		}
	}
	if err := r.requireClean(ctx, plan.Target); err != nil {
		return nil, err
	}
	from, err := r.currentBranch(ctx)
	if err != nil {
		return nil, err
	}

	result := &GateResult{}
	if plan.TargetsMain {
		outcome, err := r.repo.Resign(ctx, branchRef(plan.BuildBranch), git.ResignOptions{
			Base:     plan.ResignBase,
			SignArgs: plan.SignArgs,
			Sync:     git.SyncLocalOnly,
		})
		if err != nil {
			return nil, fmt.Errorf("vcs: re-sign %s before merging onto %s: %w", plan.BuildBranch, plan.Target, err)
		}
		result.ResignedHead = outcome.NewHead
	}

	if err := r.checkout(ctx, plan.Target); err != nil {
		return nil, err
	}
	head, mergeErr := r.mergeOnto(ctx, plan)
	if restoreErr := r.checkout(ctx, from); restoreErr != nil && mergeErr == nil {
		return nil, fmt.Errorf("vcs: merged onto %s but could not return to %s: %w", plan.Target, from, restoreErr)
	}
	if mergeErr != nil {
		return nil, mergeErr
	}
	result.Head, result.Signed = head, plan.TargetsMain
	return result, nil
}

// mergeOnto performs the merge itself, with the target already checked out,
// and returns the target's new head. Only a merge onto the main branch signs.
func (r *Repo) mergeOnto(ctx context.Context, plan GatePlan) (string, error) {
	message := plan.Message
	if message == "" {
		message = fmt.Sprintf("Merge %s into %s", plan.BuildBranch, plan.Target)
	}
	merged, err := r.repo.Merge(ctx, []string{plan.BuildBranch}, git.MergeOptions{Message: message, FastForward: git.FastForwardNever})
	if err != nil {
		return "", fmt.Errorf("vcs: merge %s into %s: %w", plan.BuildBranch, plan.Target, err)
	}
	if !plan.TargetsMain {
		return merged.NewHead, nil
	}
	return r.signCommit(ctx, plan.Target, plan.SignArgs)
}

// AddWorktree creates a node's own worktree on its own branch.
func (r *Repo) AddWorktree(ctx context.Context, path, branch, baseRef string) error {
	return r.repo.WorktreeAdd(ctx, path, baseRef, git.WorktreeAddOptions{NewBranch: branch})
}

// RemoveWorktree tears a node's worktree down once its work has merged.
func (r *Repo) RemoveWorktree(ctx context.Context, path string) error {
	return r.repo.WorktreeRemove(ctx, path, git.WorktreeRemoveOptions{Force: true})
}

// ResetWorktree returns an interrupted node's worktree to the commit it
// branched from, which is what makes replaying its stored prompt verbatim
// safe: a hard kill costs the work in flight, never the record of what came
// before it.
func (r *Repo) ResetWorktree(ctx context.Context, path, baseRef string) error {
	if _, err := r.run(ctx, path, "reset", "--hard", baseRef); err != nil {
		return fmt.Errorf("vcs: reset %s to %s: %w", path, baseRef, err)
	}
	if _, err := r.run(ctx, path, "clean", "-fdx"); err != nil {
		return fmt.Errorf("vcs: clean %s: %w", path, err)
	}
	return nil
}

// BranchHead resolves the commit a branch points at.
func (r *Repo) BranchHead(ctx context.Context, branch string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("vcs: a branch name is required")
	}
	sha, err := r.run(ctx, r.dir, "rev-parse", "--verify", branch+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("vcs: resolve %s: %w", branch, err)
	}
	return sha, nil
}

// requireBranch fails closed unless want is the branch checked out.
func (r *Repo) requireBranch(ctx context.Context, want string) error {
	got, err := r.currentBranch(ctx)
	if err != nil {
		return err
	}
	if got != want {
		return &WrongBranchError{Want: want, Got: got}
	}
	return nil
}

// currentBranch returns the branch checked out in the repository.
func (r *Repo) currentBranch(ctx context.Context) (string, error) {
	branch, err := r.run(ctx, r.dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("vcs: resolve current branch: %w", err)
	}
	return branch, nil
}

// requireClean fails closed on a working tree with uncommitted tracked
// changes. Untracked files are ignored: a node's worktree and a raw run log
// live beside the artifacts and are never committed.
func (r *Repo) requireClean(ctx context.Context, target string) error {
	out, err := r.run(ctx, r.dir, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("vcs: inspect the working tree: %w", err)
	}
	if out == "" {
		return nil
	}
	return &PreconditionError{
		Target:  target,
		Missing: fmt.Sprintf("a clean working tree; %d tracked file(s) are uncommitted and would not travel with the merge", len(strings.Split(out, "\n"))),
	}
}

// branchRef fully qualifies a branch name. Rewriting history moves the ref
// itself, and only a qualified name names one unambiguously.
func branchRef(branch string) string {
	if strings.HasPrefix(branch, "refs/") {
		return branch
	}
	return "refs/heads/" + branch
}

// checkout switches the repository to branch.
func (r *Repo) checkout(ctx context.Context, branch string) error {
	if _, err := r.run(ctx, r.dir, "checkout", branch); err != nil {
		return fmt.Errorf("vcs: check out %s: %w", branch, err)
	}
	return nil
}

// head returns the current commit.
func (r *Repo) head(ctx context.Context) (string, error) {
	sha, err := r.run(ctx, r.dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("vcs: resolve HEAD: %w", err)
	}
	return sha, nil
}

// signCommit amends the just-created merge commit into a signed equivalent
// and returns the resulting sha.
func (r *Repo) signCommit(ctx context.Context, branch string, signArgs []string) (string, error) {
	args := []string{"commit", "--amend", "--no-edit"}
	if len(signArgs) == 0 {
		args = append(args, "-S")
	} else {
		args = append(args, signArgs...)
	}
	if _, err := r.run(ctx, r.dir, args...); err != nil {
		return "", fmt.Errorf("vcs: sign the merge commit on %s: %w", branch, err)
	}
	return r.head(ctx)
}

// changedPaths lists every path that differs between two commits.
func (r *Repo) changedPaths(ctx context.Context, from, to string) ([]string, error) {
	out, err := r.run(ctx, r.dir, "diff", "--name-only", from+".."+to)
	if err != nil {
		return nil, fmt.Errorf("vcs: list changes between %s and %s: %w", from, to, err)
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// run executes one git subcommand and returns its trimmed stdout.
func (r *Repo) run(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := sysops.Run(ctx, "git", args, sysops.Options{Dir: dir})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}
