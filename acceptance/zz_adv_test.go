package acceptance_test

import (
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/acceptance"
)

func TestAdversarialFalsePositiveWordCollision(t *testing.T) {
	root := copyTree(t, repoRoot(t))
	body := read(t, root, "cmd/root.go")
	body = strings.Replace(body,
		"anoikis resume --effort my-effort",
		"anoikis resume --effort my-effort\n  # see the full list here for more, or show details",
		1)
	write(t, root, "cmd/root.go", body)

	report, err := acceptance.Run(root)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	res, ok := clause(report, "prior-spec/command-contract")
	if !ok {
		t.Fatal("clause not found")
	}
	t.Logf("met=%v violations=%v", res.Met, res.Violations)
}
