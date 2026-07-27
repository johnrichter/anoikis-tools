package ids_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/johnrichter/anoikis-tools/internal/ids"
)

// registered is every scheme the engine ships with.
func registered(t *testing.T) []ids.Scheme {
	t.Helper()
	var out []ids.Scheme
	for _, name := range ids.Names() {
		s, err := ids.Lookup(name)
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		out = append(out, s)
	}
	return out
}

func TestDefaultSchemeIsRegistered(t *testing.T) {
	if !slices.Contains(ids.Names(), ids.Default) {
		t.Fatalf("the default scheme %q is not registered; registered: %v", ids.Default, ids.Names())
	}
}

func TestUnknownSchemeIsRefusedNotDefaulted(t *testing.T) {
	s, err := ids.Lookup("no-such-scheme")
	if err == nil {
		t.Fatalf("an unregistered scheme resolved to %s instead of failing", s.Name())
	}
	if !strings.Contains(err.Error(), ids.Default) {
		t.Errorf("the error does not name what is registered: %v", err)
	}
}

func TestOpaqueReadsNoStructure(t *testing.T) {
	o := ids.Opaque{}
	for _, id := range []string{"a", "svc/one", "01JB4Z7Q", "node.v2", "любой"} {
		if err := o.Validate(id); err != nil {
			t.Errorf("opaque rejected %q: %v", id, err)
		}
	}
	for _, id := range []string{"", "   ", "has space", "with\ttab"} {
		if err := o.Validate(id); err == nil {
			t.Errorf("opaque accepted %q", id)
		}
	}
	if o.Short("svc/one") != "" {
		t.Error("opaque invented a short form")
	}
}

func TestDottedShortFormIsItsLastSegment(t *testing.T) {
	d := ids.Dotted{}
	if got := d.Short("api.store.index"); got != "index" {
		t.Errorf("short form = %q, want index", got)
	}
	if err := d.Validate("api.store.index"); err != nil {
		t.Errorf("dotted rejected a well-formed id: %v", err)
	}
	for _, id := range []string{"api..store", "API.store", "api store"} {
		if err := d.Validate(id); err == nil {
			t.Errorf("dotted accepted %q", id)
		}
	}
}

func TestDeriveIsStableAndDistinct(t *testing.T) {
	for _, s := range registered(t) {
		first, err := s.Derive([]string{"a", "b"}, "fix", 0)
		if err != nil {
			t.Fatalf("%s derive: %v", s.Name(), err)
		}
		again, err := s.Derive([]string{"a", "b"}, "fix", 0)
		if err != nil {
			t.Fatalf("%s derive again: %v", s.Name(), err)
		}
		if first != again {
			t.Errorf("%s derived %q then %q for the same inputs", s.Name(), first, again)
		}
		other, err := s.Derive([]string{"a", "b"}, "fix", 1)
		if err != nil {
			t.Fatalf("%s derive ordinal 1: %v", s.Name(), err)
		}
		if other == first {
			t.Errorf("%s derived the same id for two ordinals: %q", s.Name(), first)
		}
		different, err := s.Derive([]string{"a", "c"}, "fix", 0)
		if err != nil {
			t.Fatalf("%s derive other parents: %v", s.Name(), err)
		}
		if different == first {
			t.Errorf("%s derived the same id for different parents: %q", s.Name(), first)
		}
		if err := s.Validate(first); err != nil {
			t.Errorf("%s derived an id it then rejects: %q (%v)", s.Name(), first, err)
		}
	}
}

func TestDeriveRefusesAPunctuatedTag(t *testing.T) {
	for _, s := range registered(t) {
		if _, err := s.Derive([]string{"a"}, "fix-it", 0); err == nil {
			t.Errorf("%s accepted a punctuated graft tag", s.Name())
		}
	}
}

func TestRegisterAddsAHarnessScheme(t *testing.T) {
	ids.Register(numbered{})
	s, err := ids.Lookup("numbered")
	if err != nil {
		t.Fatalf("a registered scheme did not resolve: %v", err)
	}
	if s.Short("42") != "42" {
		t.Errorf("short form = %q, want the scheme's own answer", s.Short("42"))
	}
}

// numbered is a throwaway harness scheme, standing in for whatever id
// vocabulary a real harness brings.
type numbered struct{}

func (numbered) Name() string { return "numbered" }

func (numbered) Validate(id string) error {
	if id == "" {
		return errEmpty
	}
	return nil
}

func (numbered) Short(id string) string { return id }

func (numbered) Derive(_ []string, tag string, ordinal int) (string, error) {
	return tag + "-" + strings.Repeat("0", ordinal) + "1", nil
}

var errEmpty = stringError("numbered: id must not be empty")

type stringError string

func (e stringError) Error() string { return string(e) }
