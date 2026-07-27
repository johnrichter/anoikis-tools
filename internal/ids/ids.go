// Package ids is the node-identifier seam.
//
// The engine keys its graph on opaque strings and asks a Scheme for every
// judgement it needs about one — is this id well-formed, what is its short
// form, what should a node grafted onto these parents be called. No id
// grammar is compiled into the engine, so an effort can adopt whatever id
// vocabulary suits it by naming a scheme in its harness policy.
//
// Two schemes ship. Opaque imposes no structure at all beyond printability
// and is the right default when ids come from elsewhere. Dotted gives ids a
// hierarchy whose last component is a usable short form. A harness with its
// own vocabulary registers a third.
package ids

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/johnrichter/claude-shared-tooling/go/graph"
)

// MaxLength bounds an id, matching the artifact schemas' own token bound so a
// scheme can never mint an id the contract would reject.
const MaxLength = 128

// Scheme is everything the engine knows about node ids.
type Scheme interface {
	// Name is the scheme's registry key, as a harness policy names it.
	Name() string

	// Validate reports whether id is well-formed under this scheme. The
	// error is caller-visible, so it states what the scheme expected.
	Validate(id string) error

	// Short returns the abbreviated form a reference may use in place of id,
	// or "" when this scheme gives ids no short form. A short form is only
	// ever honoured when exactly one node answers to it.
	Short(id string) string

	// Derive returns the id for a node the engine inserts itself, grafted
	// onto parents. tag names why the node exists ("fix"); ordinal separates
	// repeated grafts onto the same parents. The result is stable for the
	// same inputs and always passes Validate.
	Derive(parents []string, tag string, ordinal int) (string, error)
}

// Opaque accepts any printable, non-blank id and reads no structure into it.
type Opaque struct{}

// Name returns the registry key "opaque".
func (Opaque) Name() string { return "opaque" }

// Validate accepts a non-blank id of printable characters within MaxLength.
func (Opaque) Validate(id string) error { return validatePrintable(id) }

// Short returns "": an opaque id has no abbreviated form to offer.
func (Opaque) Short(string) string { return "" }

// Derive names the graft after its tag and a digest of its parents, so the
// id is stable, collision-resistant, and carries no structure Opaque does not
// otherwise claim.
func (o Opaque) Derive(parents []string, tag string, ordinal int) (string, error) {
	if err := validateTag(tag); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%d", tag, parentDigest(parents), ordinal), nil
}

// Dotted reads an id as dot-separated segments: a hierarchy whose last
// segment is its short form.
type Dotted struct{}

// Name returns the registry key "dotted".
func (Dotted) Name() string { return "dotted" }

// Validate accepts one or more dot-separated segments of lowercase
// alphanumerics and hyphens.
func (Dotted) Validate(id string) error {
	if err := validatePrintable(id); err != nil {
		return err
	}
	for _, seg := range strings.Split(id, ".") {
		if seg == "" {
			return fmt.Errorf("ids: dotted id %q has an empty segment", id)
		}
		for _, r := range seg {
			if r != '-' && !isLowerAlnum(r) {
				return fmt.Errorf("ids: dotted id %q has an invalid character %q; segments allow lowercase letters, digits and hyphens", id, r)
			}
		}
	}
	return nil
}

// Short returns the id's last segment.
func (Dotted) Short(id string) string {
	if i := strings.LastIndex(id, "."); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return id
}

// Derive extends the first parent with a tag segment, keeping a graft
// adjacent to the work it came from in any lexical ordering.
func (d Dotted) Derive(parents []string, tag string, ordinal int) (string, error) {
	if err := validateTag(tag); err != nil {
		return "", err
	}
	prefix := "graft"
	if len(parents) > 0 && parents[0] != "" {
		prefix = parents[0]
	}
	id := fmt.Sprintf("%s.%s-%s-%d", prefix, tag, parentDigest(parents), ordinal)
	if err := d.Validate(id); err != nil {
		return "", err
	}
	return id, nil
}

// Default is the scheme used when a harness policy names none.
const Default = "opaque"

var (
	registryMu sync.RWMutex
	registry   = map[string]Scheme{
		Opaque{}.Name(): Opaque{},
		Dotted{}.Name(): Dotted{},
	}
)

// Register adds s under its own name, replacing any scheme already
// registered under that name. A harness with its own id vocabulary calls this
// before the engine resolves its policy.
func Register(s Scheme) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[s.Name()] = s
}

// Lookup returns the scheme registered under name. An unknown name is an
// error naming what is registered, never a silent fallback to the default —
// a plan validated under one id vocabulary must not be scheduled under
// another.
func Lookup(name string) (Scheme, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("ids: no scheme named %q; registered: %s", name, strings.Join(slices.Sorted(maps.Keys(registry)), ", "))
	}
	return s, nil
}

// Names returns every registered scheme name, sorted.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return slices.Sorted(maps.Keys(registry))
}

// GraphScheme adapts s to the id description graph needs: canonical text and
// the optional short form it resolves references through.
func GraphScheme(s Scheme) graph.IDScheme[string] {
	return graph.IDScheme[string]{
		String: func(id string) string { return id },
		Short:  s.Short,
	}
}

// validatePrintable enforces the bounds every scheme shares: non-blank,
// within MaxLength, no spaces and no control characters.
func validatePrintable(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("ids: id must not be blank")
	}
	if len(id) > MaxLength {
		return fmt.Errorf("ids: id is %d bytes, max %d", len(id), MaxLength)
	}
	for _, r := range id {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return fmt.Errorf("ids: id %q contains a space or control character", id)
		}
	}
	return nil
}

// validateTag enforces that a graft tag is a bare lowercase word, so a
// derived id never inherits punctuation a scheme would then reject.
func validateTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("ids: graft tag must not be empty")
	}
	for _, r := range tag {
		if !isLowerAlnum(r) {
			return fmt.Errorf("ids: graft tag %q must be lowercase letters and digits", tag)
		}
	}
	return nil
}

// isLowerAlnum reports whether r is a lowercase letter or a digit — the
// character class both shipped schemes build their grammars from.
func isLowerAlnum(r rune) bool {
	return unicode.IsDigit(r) || (unicode.IsLower(r) && unicode.IsLetter(r))
}

// parentDigest is a short, order-sensitive fingerprint of the parents a graft
// hangs from: enough to keep two grafts from colliding, short enough to keep
// the derived id readable.
func parentDigest(parents []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parents, "\x00")))
	return hex.EncodeToString(sum[:4])
}
