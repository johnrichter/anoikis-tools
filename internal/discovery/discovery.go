// Package discovery locates a project's documents by what they declare about
// themselves, never by what they are named.
//
// A project home is a flat directory of Markdown documents, each optionally
// carrying a leading YAML frontmatter block with a "tags" list of
// "namespace:value" entries. Two namespaces matter here: "type" names what
// kind of document a file is, and "status" names where it stands. Matching a
// fixed, assumed filename instead is a hidden coupling between a tool and a
// naming convention neither owns: an operator-authored design saved under
// any other name would go unrecognized and silently fall back to the wrong
// route. Reading the declared type removes that coupling; a document is the
// design because it says so, not because of where it sits.
package discovery

import "fmt"

// DocType is a project document's declared kind, read from its "type"
// frontmatter tag.
type DocType string

// Design is the declared type this package resolves the design-derivation
// gathering step against.
const Design DocType = "design"

// Status is a document's declared lifecycle stage, read from its "status"
// frontmatter tag.
type Status string

// The closed status vocabulary a design document may declare.
const (
	Stub     Status = "stub"
	Complete Status = "complete"
)

// Route is what a design document's declared status routes a caller to do
// next.
type Route string

const (
	// Derive means the design is complete: derive a plan from it.
	Derive Route = "derive"
	// ResumeDraft means the design is a stub: resume drafting it.
	ResumeDraft Route = "resume-draft"
)

// Named discovery failures. Each names exactly what is missing or ambiguous
// and where; none of them degrades into a guess from a filename.
var (
	// ErrTypeUndetermined reports that no document under the project home
	// declares the wanted type. The missing field is "type": either no
	// document declared one at all, or none declared a recognized value.
	ErrTypeUndetermined = fmt.Errorf("discovery: no document declares the \"type\" tag being looked for")

	// ErrAmbiguousType reports more than one document declaring the same
	// type. Locate refuses rather than taking the first match; the error
	// names every candidate.
	ErrAmbiguousType = fmt.Errorf("discovery: more than one document declares the same type")

	// ErrStatusUndetermined reports a resolved document whose "status" tag
	// is absent or outside the closed vocabulary this package routes.
	ErrStatusUndetermined = fmt.Errorf("discovery: document's \"status\" tag is absent or unrecognized")
)

// Candidate is one document under a project home, described entirely by what
// its own frontmatter declares — never by its path.
type Candidate struct {
	// Path is where the document lives. It is carried only for error
	// messages and so a caller can read the document back; it plays no part
	// in selection.
	Path string
	// Type is the document's declared "type" tag value, empty when
	// undeclared.
	Type string
	// Status is the document's declared "status" tag value, empty when
	// undeclared.
	Status string
}

// LocateDesign runs the full gathering step for a project's design document:
// enumerate every document under dir, select the one declaring type Design,
// and route on its declared status. This is the one entry point the
// gathering step needs — it never inspects a filename.
func LocateDesign(dir string) (Candidate, Route, error) {
	candidates, err := Gather(dir)
	if err != nil {
		return Candidate{}, "", err
	}
	design, err := Select(candidates, Design)
	if err != nil {
		return Candidate{}, "", err
	}
	route, err := ClassifyStatus(design.Status)
	if err != nil {
		return Candidate{}, "", fmt.Errorf("%w (document: %s)", err, design.Path)
	}
	return design, route, nil
}
