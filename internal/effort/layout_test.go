package effort_test

import (
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/effort"
)

// TestReviewFindingsNeverCollidesWithFindings guards the path split a
// reviewer's brief depends on: the review-verdict artifact and the effort's
// durable findings register must never resolve to the same file, for any
// effort or any review id, or a reviewer with no schema to consult could once
// again clobber the register by picking the wrong conventional path.
func TestReviewFindingsNeverCollidesWithFindings(t *testing.T) {
	slugs := []string{"e", "my-effort", "toolbelt", "a.b_c-123"}
	reviewIDs := []string{"findings", "node-1", "cli-and-docs", "gate-review-1"}
	for _, slug := range slugs {
		l := effort.Layout{Root: "/root", Slug: slug}
		for _, id := range reviewIDs {
			if got := l.ReviewFindings(id); got == l.Findings() {
				t.Fatalf("effort %q: ReviewFindings(%q) = %q, collides with Findings()", slug, id, got)
			}
		}
	}
}
