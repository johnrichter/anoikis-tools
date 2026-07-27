package discovery

import (
	"fmt"
	"strings"
)

// Select finds the one candidate among candidates that declares want,
// refusing ambiguity rather than taking a silent first match, and naming
// what is missing when none do.
func Select(candidates []Candidate, want DocType) (Candidate, error) {
	var matches []Candidate
	for _, c := range candidates {
		if c.Type == string(want) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 0:
		return Candidate{}, fmt.Errorf("%w: wanted type %q", ErrTypeUndetermined, want)
	case 1:
		return matches[0], nil
	default:
		paths := make([]string, len(matches))
		for i, m := range matches {
			paths[i] = m.Path
		}
		return Candidate{}, fmt.Errorf("%w: type %q declared by %s", ErrAmbiguousType, want, strings.Join(paths, ", "))
	}
}
