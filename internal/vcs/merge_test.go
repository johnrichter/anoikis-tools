package vcs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johnrichter/anoikis-tools/internal/dag"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func commitFile(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	writeFile(t, dir, name, content)
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", message)
	return runGit(t, dir, "rev-parse", "HEAD")
}

// newScratchRepo creates a throwaway repo on branch main, with a bare git
// identity and signing disabled, then opens it through vcs.Open.
func newScratchRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	commitFile(t, dir, "root.txt", "root\n", "root")
	r, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open scratch repo: %v", err)
	}
	return r, dir
}

func branchFrom(t *testing.T, dir, base, branch string) {
	t.Helper()
	runGit(t, dir, "checkout", "-q", "-b", branch, base)
}

// --- MergeLayer ---

func TestMergeLayerOctopusMergesDisjointBranches(t *testing.T) {
	r, dir := newScratchRepo(t)
	ctx := context.Background()
	runGit(t, dir, "checkout", "-q", "-b", "build")
	base := runGit(t, dir, "rev-parse", "HEAD")

	branchFrom(t, dir, base, "node-a")
	commitFile(t, dir, "a.txt", "a\n", "node a")
	branchFrom(t, dir, base, "node-b")
	commitFile(t, dir, "b.txt", "b\n", "node b")
	runGit(t, dir, "checkout", "-q", "build")

	res, err := r.MergeLayer(ctx, LayerPlan{BuildBranch: "build", Branches: []string{"node-a", "node-b"}})
	if err != nil {
		t.Fatalf("MergeLayer: %v", err)
	}
	if res.Base != base {
		t.Fatalf("Base = %s, want %s", res.Base, base)
	}
	got := map[string]bool{}
	for _, p := range res.Changed {
		got[p] = true
	}
	if !got["a.txt"] || !got["b.txt"] {
		t.Fatalf("Changed = %v, want a.txt and b.txt", res.Changed)
	}
	for _, want := range []string{"a.txt", "b.txt", "root.txt"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected %s on disk after merge: %v", want, err)
		}
	}
}

// TestMergeLayerConflictLeavesBuildBranchUntouched: an octopus merge that
// cannot resolve must fail closed rather than leave a half-merged tree.
func TestMergeLayerConflictLeavesBuildBranchUntouched(t *testing.T) {
	r, dir := newScratchRepo(t)
	ctx := context.Background()
	runGit(t, dir, "checkout", "-q", "-b", "build")
	base := runGit(t, dir, "rev-parse", "HEAD")

	branchFrom(t, dir, base, "node-a")
	commitFile(t, dir, "root.txt", "a-version\n", "node a edits root")
	branchFrom(t, dir, base, "node-b")
	commitFile(t, dir, "root.txt", "b-version\n", "node b edits root")
	runGit(t, dir, "checkout", "-q", "build")

	_, err := r.MergeLayer(ctx, LayerPlan{BuildBranch: "build", Branches: []string{"node-a", "node-b"}})
	if err == nil {
		t.Fatal("MergeLayer over a real conflict: want error, got nil")
	}
	headAfter := runGit(t, dir, "rev-parse", "HEAD")
	if headAfter != base {
		t.Fatalf("build branch head moved to %s despite a failed merge (was %s)", headAfter, base)
	}
	status := runGit(t, dir, "status", "--porcelain")
	if status != "" {
		t.Fatalf("working tree left dirty after a failed merge: %q", status)
	}
}

func TestMergeLayerRefusesWrongBranchCheckedOut(t *testing.T) {
	r, dir := newScratchRepo(t)
	ctx := context.Background()
	runGit(t, dir, "checkout", "-q", "-b", "build")
	base := runGit(t, dir, "rev-parse", "HEAD")
	branchFrom(t, dir, base, "node-a")
	commitFile(t, dir, "a.txt", "a\n", "node a")
	runGit(t, dir, "checkout", "-q", "node-a") // wrong branch checked out

	_, err := r.MergeLayer(ctx, LayerPlan{BuildBranch: "build", Branches: []string{"node-a"}})
	var wrongBranch *WrongBranchError
	if err == nil {
		t.Fatal("MergeLayer with the wrong branch checked out: want error, got nil")
	}
	if !asWrongBranchError(err, &wrongBranch) {
		t.Fatalf("MergeLayer error = %v, want *WrongBranchError", err)
	}
	if wrongBranch.Want != "build" || wrongBranch.Got != "node-a" {
		t.Fatalf("WrongBranchError = %+v, want Want=build Got=node-a", wrongBranch)
	}
}

func TestMergeLayerRefusesEmptyBranchList(t *testing.T) {
	r, dir := newScratchRepo(t)
	runGit(t, dir, "checkout", "-q", "-b", "build")
	if _, err := r.MergeLayer(context.Background(), LayerPlan{BuildBranch: "build"}); err == nil {
		t.Fatal("MergeLayer with no branches: want error, got nil")
	}
}

func asWrongBranchError(err error, target **WrongBranchError) bool {
	e, ok := err.(*WrongBranchError)
	if ok {
		*target = e
	}
	return ok
}

// --- MergeGate: non-main target ---

func TestMergeGateNonMainMergesUnsignedAndRestoresBranch(t *testing.T) {
	r, dir := newScratchRepo(t)
	ctx := context.Background()
	runGit(t, dir, "checkout", "-q", "-b", "integration")
	runGit(t, dir, "checkout", "-q", "-b", "build")
	commitFile(t, dir, "build.txt", "build\n", "build work")
	// Driver stays on build after the gate merge dispatches.

	res, err := r.MergeGate(ctx, GatePlan{BuildBranch: "build", Target: "integration"})
	if err != nil {
		t.Fatalf("MergeGate: %v", err)
	}
	if res.Signed {
		t.Fatal("non-main gate merge reported Signed = true")
	}
	if res.ResignedHead != "" {
		t.Fatalf("non-main gate merge resigned anything: %q", res.ResignedHead)
	}
	back := runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if back != "build" {
		t.Fatalf("checked-out branch after MergeGate = %s, want build (restored)", back)
	}
	runGit(t, dir, "checkout", "-q", "integration")
	if _, err := os.Stat(filepath.Join(dir, "build.txt")); err != nil {
		t.Fatalf("build.txt missing from integration after merge: %v", err)
	}
}

func TestMergeGateRefusesNoTarget(t *testing.T) {
	r, dir := newScratchRepo(t)
	runGit(t, dir, "checkout", "-q", "-b", "build")
	if _, err := r.MergeGate(context.Background(), GatePlan{BuildBranch: "build", Target: dag.MergeTargetNone}); err == nil {
		t.Fatal("MergeGate with target none: want error, got nil")
	}
	if _, err := r.MergeGate(context.Background(), GatePlan{BuildBranch: "build"}); err == nil {
		t.Fatal("MergeGate with empty target: want error, got nil")
	}
}

func TestMergeGateRefusesDirtyWorkingTree(t *testing.T) {
	r, dir := newScratchRepo(t)
	runGit(t, dir, "checkout", "-q", "-b", "integration")
	runGit(t, dir, "checkout", "-q", "-b", "build")
	commitFile(t, dir, "build.txt", "build\n", "build work")
	writeFile(t, dir, "root.txt", "dirty\n") // tracked, uncommitted

	_, err := r.MergeGate(context.Background(), GatePlan{BuildBranch: "build", Target: "integration"})
	var precondition *PreconditionError
	if !asPreconditionError(err, &precondition) {
		t.Fatalf("MergeGate over a dirty tree: got %v, want *PreconditionError", err)
	}
}

func TestMergeGateUntrackedFilesDoNotBlock(t *testing.T) {
	r, dir := newScratchRepo(t)
	runGit(t, dir, "checkout", "-q", "-b", "integration")
	runGit(t, dir, "checkout", "-q", "-b", "build")
	commitFile(t, dir, "build.txt", "build\n", "build work")
	writeFile(t, dir, "scratch.log", "not tracked\n")

	if _, err := r.MergeGate(context.Background(), GatePlan{BuildBranch: "build", Target: "integration"}); err != nil {
		t.Fatalf("MergeGate with only untracked noise present: %v", err)
	}
}

// --- MergeGate: main target ---

func TestMergeGateMainRequiresMessageAndResignBase(t *testing.T) {
	r, dir := newScratchRepo(t)
	runGit(t, dir, "checkout", "-q", "-b", "build")
	commitFile(t, dir, "build.txt", "build\n", "build work")

	var precondition *PreconditionError
	_, err := r.MergeGate(context.Background(), GatePlan{BuildBranch: "build", Target: "main", TargetsMain: true})
	if !asPreconditionError(err, &precondition) {
		t.Fatalf("MergeGate onto main with no message/base: got %v, want *PreconditionError", err)
	}

	_, err = r.MergeGate(context.Background(), GatePlan{
		BuildBranch: "build", Target: "main", TargetsMain: true, Message: "approved",
	})
	if !asPreconditionError(err, &precondition) {
		t.Fatalf("MergeGate onto main with a message but no resign base: got %v, want *PreconditionError", err)
	}
}

// TestMergeGateMainResignsAndSigns exercises the full main-branch path with a
// real ephemeral key: re-sign moves the build branch, the merge onto main
// re-parents correctly, and the final commit is a real, verifiable signature
// — not merely a code path that ran without checking what it produced.
func TestMergeGateMainResignsAndSigns(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed; skipping real-signature MergeGate test")
	}
	fingerprint, gnupgHome := genEphemeralGPGKey(t)
	t.Setenv("GNUPGHOME", gnupgHome)

	r, dir := newScratchRepo(t)
	ctx := context.Background()
	runGit(t, dir, "config", "gpg.format", "openpgp")
	runGit(t, dir, "config", "user.signingkey", fingerprint)

	base := runGit(t, dir, "rev-parse", "HEAD")
	runGit(t, dir, "checkout", "-q", "-b", "build")
	commitFile(t, dir, "build.txt", "build\n", "build work")

	res, err := r.MergeGate(ctx, GatePlan{
		BuildBranch: "build",
		Target:      "main",
		TargetsMain: true,
		Message:     "operator approved this merge",
		ResignBase:  base,
	})
	if err != nil {
		t.Fatalf("MergeGate onto main: %v", err)
	}
	if !res.Signed {
		t.Fatal("main-target GateResult.Signed = false")
	}
	if res.ResignedHead == "" {
		t.Fatal("main-target GateResult.ResignedHead is empty")
	}
	verify := exec.Command("git", "verify-commit", res.Head)
	verify.Dir = dir
	verify.Env = append(os.Environ(), "GNUPGHOME="+gnupgHome)
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("git verify-commit %s: %v\n%s", res.Head, err, out)
	}
	back := runGit(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if back != "build" {
		t.Fatalf("checked-out branch after main merge = %s, want build (restored)", back)
	}
	msg := runGit(t, dir, "log", "-1", "--format=%s", "main")
	if msg != "operator approved this merge" {
		t.Fatalf("main merge commit message = %q, want the operator-approved text", msg)
	}
}

func asPreconditionError(err error, target **PreconditionError) bool {
	e, ok := err.(*PreconditionError)
	if ok {
		*target = e
	}
	return ok
}

// genEphemeralGPGKey creates a throwaway, no-passphrase GPG key in an
// isolated GNUPGHOME so a signing test never touches the host's real
// keyring. It skips the test if gpg isn't installed.
func genEphemeralGPGKey(t *testing.T) (fingerprint, gnupgHome string) {
	t.Helper()
	gnupgHome = filepath.Join(t.TempDir(), "gnupg")
	if err := os.MkdirAll(gnupgHome, 0o700); err != nil {
		t.Fatalf("mkdir GNUPGHOME: %v", err)
	}
	batch := filepath.Join(t.TempDir(), "keygen.batch")
	script := "%no-protection\n" +
		"Key-Type: eddsa\nKey-Curve: ed25519\n" +
		"Subkey-Type: eddsa\nSubkey-Curve: ed25519\n" +
		"Name-Real: Vcs Test\nName-Email: vcs-test@example.com\n" +
		"Expire-Date: 0\n%commit\n"
	if err := os.WriteFile(batch, []byte(script), 0o600); err != nil {
		t.Fatalf("write keygen batch: %v", err)
	}
	cmd := exec.Command("gpg", "--batch", "--gen-key", batch)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+gnupgHome)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gpg --gen-key: %v\n%s", err, out)
	}
	cmd = exec.Command("gpg", "--with-colons", "--list-secret-keys")
	cmd.Env = append(os.Environ(), "GNUPGHOME="+gnupgHome)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("gpg --list-secret-keys: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "fpr:") {
			fields := strings.Split(line, ":")
			if len(fields) > 9 {
				return fields[9], gnupgHome
			}
		}
	}
	t.Fatalf("no fingerprint found in gpg output:\n%s", out)
	return "", ""
}

// --- BranchHead / worktrees ---

func TestBranchHeadResolvesAndRejectsEmpty(t *testing.T) {
	r, dir := newScratchRepo(t)
	ctx := context.Background()
	want := runGit(t, dir, "rev-parse", "main")
	got, err := r.BranchHead(ctx, "main")
	if err != nil || got != want {
		t.Fatalf("BranchHead(main) = %q, %v; want %q, nil", got, err, want)
	}
	if _, err := r.BranchHead(ctx, ""); err == nil {
		t.Fatal("BranchHead(\"\"): want error, got nil")
	}
	if _, err := r.BranchHead(ctx, "does-not-exist"); err == nil {
		t.Fatal("BranchHead of an unknown branch: want error, got nil")
	}
}

func TestAddResetRemoveWorktreeRoundTrip(t *testing.T) {
	r, dir := newScratchRepo(t)
	ctx := context.Background()
	base := runGit(t, dir, "rev-parse", "main")
	wtPath := filepath.Join(t.TempDir(), "node-wt")

	if err := r.AddWorktree(ctx, wtPath, "node-x", base); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	writeFile(t, wtPath, "scratch.txt", "in flight\n")
	runGit(t, wtPath, "add", "-A")

	if err := r.ResetWorktree(ctx, wtPath, base); err != nil {
		t.Fatalf("ResetWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("ResetWorktree left uncommitted work behind: stat err = %v", err)
	}
	head := runGit(t, wtPath, "rev-parse", "HEAD")
	if head != base {
		t.Fatalf("worktree HEAD after reset = %s, want base %s", head, base)
	}

	if err := r.RemoveWorktree(ctx, wtPath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still present after RemoveWorktree")
	}
}

// --- Backstop ---

func TestBackstopRefusesEmptyCommand(t *testing.T) {
	r, _ := newScratchRepo(t)
	if _, err := r.Backstop(context.Background(), nil, time.Second); err == nil {
		t.Fatal("Backstop with no command: want error, got nil")
	}
}

func TestBackstopReportsPassAndFail(t *testing.T) {
	r, _ := newScratchRepo(t)
	ctx := context.Background()

	ok, err := r.Backstop(ctx, []string{"true"}, time.Second)
	if err != nil || !ok.Passed || ok.ExitCode != 0 {
		t.Fatalf("Backstop(true) = %+v, %v; want Passed", ok, err)
	}

	fail, err := r.Backstop(ctx, []string{"sh", "-c", "echo boom 1>&2; exit 3"}, time.Second)
	if err != nil {
		t.Fatalf("Backstop(failing command) transport error: %v", err)
	}
	if fail.Passed || fail.ExitCode != 3 {
		t.Fatalf("Backstop(failing command) = %+v, want Passed=false ExitCode=3", fail)
	}
	if !strings.Contains(fail.Excerpt, "boom") {
		t.Fatalf("Backstop excerpt = %q, want it to contain the command's output", fail.Excerpt)
	}
}

func TestBackstopExcerptIsTruncatedNotUnbounded(t *testing.T) {
	r, _ := newScratchRepo(t)
	script := "python3 -c \"print('x'*20000)\" 2>/dev/null || yes x | head -c 20000; exit 1"
	res, err := r.Backstop(context.Background(), []string{"sh", "-c", script}, 5*time.Second)
	if err != nil {
		t.Fatalf("Backstop: %v", err)
	}
	if len(res.Excerpt) > backstopExcerpt+64 {
		t.Fatalf("Backstop excerpt length = %d, want bounded near %d", len(res.Excerpt), backstopExcerpt)
	}
}

// --- AssertSurfaces / matches ---

func TestAssertSurfacesFlagsUndeclaredDrift(t *testing.T) {
	nodes := map[string][]dag.Claim{
		"node-a": {{Domain: "path", Kind: "dir", Value: "pkg/a"}},
		"node-b": {{Domain: "path", Kind: "file", Value: "pkg/b/only.go"}},
	}
	changed := []string{"pkg/a/x.go", "pkg/b/only.go", "pkg/c/surprise.go"}
	drift := AssertSurfaces(changed, nodes, []string{"path"})
	if len(drift) != 1 || drift[0] != "pkg/c/surprise.go" {
		t.Fatalf("AssertSurfaces drift = %v, want exactly [pkg/c/surprise.go]", drift)
	}
}

func TestAssertSurfacesGlobMatchesTheAdmissionDialect(t *testing.T) {
	nodes := map[string][]dag.Claim{
		"node-a": {{Domain: "path", Kind: "glob", Value: "pkg/**/*.go"}},
	}
	changed := []string{"pkg/deep/nested/file.go", "docs/readme.md"}
	drift := AssertSurfaces(changed, nodes, []string{"path"})
	if len(drift) != 1 || drift[0] != "docs/readme.md" {
		t.Fatalf("AssertSurfaces drift = %v, want exactly [docs/readme.md]", drift)
	}
}

// TestAssertSurfacesIgnoresNonPathDomains: a namespace claim has no diff
// counterpart, so a changed path can never be "covered" by one — the caller
// must pass only the path-shaped domains it actually wants checked.
func TestAssertSurfacesIgnoresNonPathDomains(t *testing.T) {
	nodes := map[string][]dag.Claim{
		"node-a": {{Domain: "namespace", Kind: "file", Value: "pkg/a/x.go"}},
	}
	changed := []string{"pkg/a/x.go"}
	drift := AssertSurfaces(changed, nodes, []string{"path"})
	if len(drift) != 1 {
		t.Fatalf("AssertSurfaces drift = %v, want the path flagged since only the namespace domain declared it", drift)
	}
}

func TestAssertSurfacesEmptyPathIgnored(t *testing.T) {
	drift := AssertSurfaces([]string{""}, map[string][]dag.Claim{}, []string{"path"})
	if len(drift) != 0 {
		t.Fatalf("AssertSurfaces([\"\"]) = %v, want empty (no path to drift on)", drift)
	}
}

func TestAssertSurfacesUnknownClaimKindCoversNothing(t *testing.T) {
	nodes := map[string][]dag.Claim{
		"node-a": {{Domain: "path", Kind: "regex", Value: "pkg/.*"}},
	}
	drift := AssertSurfaces([]string{"pkg/x.go"}, nodes, []string{"path"})
	if len(drift) != 1 {
		t.Fatalf("AssertSurfaces with an undecidable claim kind = %v, want the path flagged as drift", drift)
	}
}
