package vcs

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestBackstopInvokesTheReleasedLanguageToolsCLI is the runtime-edge proof
// for the harness's post-merge backstop: the harness policy's real backstop
// command names a language-tools CLI invocation (see
// examples/harness-policy.json's "backstop" field), and this exercises that
// edge against the actual released artifact rather than a source build --
// the same per-OS/arch archive plugin/hooks/download-script.sh would fetch
// and verify at runtime.
//
// It skips (never fails) when no released language-tools archive is found
// beside this checkout: the artifact is a build output of a sibling repo's
// own release task, not something this repo's test suite produces, and a
// checkout of anoikis-tools alone (CI, a fresh clone) has no sibling to find.
func TestBackstopInvokesTheReleasedLanguageToolsCLI(t *testing.T) {
	binPath := resolveReleasedLanguageToolsBinary(t)

	r, dir := newScratchRepo(t)
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"backstop-fixture\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[[bin]]\nname = \"backstop-fixture\"\npath = \"src/main.rs\"\n")
	writeFile(t, dir, "src/main.rs", "fn main() {\n    println!(\"backstop fixture\");\n}\n")

	res, err := r.Backstop(context.Background(), []string{binPath, "build", "--language", "rust", "--dir", "."}, 2*time.Minute)
	if err != nil {
		t.Fatalf("Backstop via the released language-tools binary: %v", err)
	}
	if !res.Passed {
		t.Fatalf("Backstop via the released language-tools binary did not pass: %+v", res)
	}
}

// resolveReleasedLanguageToolsBinary locates the language-tools sibling
// repo's released dist archive for this host's GOOS/GOARCH, extracts its
// binary to a temp dir and returns the extracted path. It skips the calling
// test when no archive is found rather than failing: this repo does not own
// language-tools' release artifact, only the edge that calls it.
func resolveReleasedLanguageToolsBinary(t *testing.T) string {
	t.Helper()

	var candidates []string
	if override := os.Getenv("ANOIKIS_TEST_LANGUAGE_TOOLS_DIST"); override != "" {
		candidates = append(candidates, override)
	}
	if siblingRoot, ok := findSiblingRepoRoot(t, "language-tools"); ok {
		candidates = append(candidates,
			filepath.Join(siblingRoot, "dist"),
			filepath.Join(siblingRoot, ".claude", "worktrees", "toolbelt", "dist"),
		)
	}

	pattern := fmt.Sprintf("language-tools_v*_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for _, dir := range candidates {
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(matches) == 0 {
			continue
		}
		return extractBinaryFromArchive(t, matches[0], "language-tools")
	}
	t.Skip("no released language-tools dist archive found beside this checkout; skipping the runtime-edge proof")
	return ""
}

// findSiblingRepoRoot walks up from this test file's own directory to the
// checkout root named repoName (this repo, anoikis-tools) is under, then
// returns the sibling checkout of siblingName at the same level.
func findSiblingRepoRoot(t *testing.T, siblingName string) (string, bool) {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	dir := filepath.Dir(thisFile)
	for {
		parent := filepath.Dir(dir)
		if filepath.Base(dir) == "anoikis-tools" {
			return filepath.Join(parent, siblingName), true
		}
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// extractBinaryFromArchive extracts the file named binName from the gzipped
// tar archive at archivePath into a temp dir and returns its path, made
// executable.
func extractBinaryFromArchive(t *testing.T, archivePath, binName string) string {
	t.Helper()
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open release archive %s: %v", archivePath, err)
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip release archive %s: %v", archivePath, err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("release archive %s does not contain %q", archivePath, binName)
		}
		if hdr.Name != binName {
			continue
		}
		outPath := filepath.Join(t.TempDir(), binName)
		out, err := os.OpenFile(outPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			t.Fatalf("create extracted binary %s: %v", outPath, err)
		}
		if _, err := out.ReadFrom(tr); err != nil {
			out.Close()
			t.Fatalf("extract %s from %s: %v", binName, archivePath, err)
		}
		if err := out.Close(); err != nil {
			t.Fatalf("close extracted binary %s: %v", outPath, err)
		}
		return outPath
	}
}
