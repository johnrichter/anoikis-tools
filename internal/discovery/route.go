package discovery

import "fmt"

// ClassifyStatus composes the design status→route decision this package's
// callers need: a complete design is ready to derive a plan from, a stub
// design is still being drafted. This is the one place that mapping lives —
// Gather and Select only observe and select a document, they never re-derive
// what its status means.
func ClassifyStatus(status string) (Route, error) {
	switch Status(status) {
	case Complete:
		return Derive, nil
	case Stub:
		return ResumeDraft, nil
	default:
		return "", fmt.Errorf("%w: got %q", ErrStatusUndetermined, status)
	}
}
