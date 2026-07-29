package acceptance

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// unscanned are directories a conformance check never descends into: version
// control and editor state, release output, and dependency trees. Everything
// else in the checkout is source this gate is entitled to read.
var unscanned = map[string]bool{
	".git":         true,
	".claude":      true,
	"dist":         true,
	"node_modules": true,
}

// Tree is one checkout, presented as the flat, sorted file list a clause check
// works over.
//
// Every read is cached and every listing is sorted, so a clause that reports a
// violation reports it in the same order on every run — which is what lets the
// report be committed and compared byte for byte. Nothing here follows a
// symlink: a sibling checkout linked into the tree for local development is
// not part of it.
type Tree struct {
	root  string
	paths []string
	files map[string][]byte
	asts  map[string]*ast.File
	fset  *token.FileSet
}

// OpenTree indexes the checkout at root.
func OpenTree(root string) (*Tree, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("acceptance: resolve %s: %w", root, err)
	}
	t := &Tree{root: abs, files: map[string][]byte{}, asts: map[string]*ast.File{}, fset: token.NewFileSet()}
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if unscanned[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		t.paths = append(t.paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("acceptance: index %s: %w", abs, err)
	}
	if len(t.paths) == 0 {
		return nil, fmt.Errorf("acceptance: %s holds no files to check", abs)
	}
	slices.Sort(t.paths)
	return t, nil
}

// Root is the checkout this tree indexes.
func (t *Tree) Root() string { return t.root }

// Paths returns every indexed file, sorted.
func (t *Tree) Paths() []string { return t.paths }

// WithExt returns every indexed file carrying the given extension, sorted.
func (t *Tree) WithExt(ext string) []string {
	var out []string
	for _, p := range t.paths {
		if filepath.Ext(p) == ext {
			out = append(out, p)
		}
	}
	return out
}

// GoSource returns every Go file, sorted. Test files are included only when
// asked for: a check about the shipped API has no business reading them.
func (t *Tree) GoSource(withTests bool) []string {
	var out []string
	for _, p := range t.WithExt(".go") {
		if !withTests && strings.HasSuffix(p, "_test.go") {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Has reports whether the tree holds the named file.
func (t *Tree) Has(rel string) bool { return slices.Contains(t.paths, rel) }

// Read returns the named file's bytes.
func (t *Tree) Read(rel string) ([]byte, error) {
	if raw, ok := t.files[rel]; ok {
		return raw, nil
	}
	raw, err := os.ReadFile(filepath.Join(t.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, fmt.Errorf("acceptance: read %s: %w", rel, err)
	}
	t.files[rel] = raw
	return raw, nil
}

// Text returns the named file as a string, empty when it cannot be read. It
// suits a check that treats an unreadable file as carrying nothing, which is
// every check that searches for something rather than requiring it.
func (t *Tree) Text(rel string) string {
	raw, err := t.Read(rel)
	if err != nil {
		return ""
	}
	return string(raw)
}

// JSON decodes the named file into out.
func (t *Tree) JSON(rel string, out any) error {
	raw, err := t.Read(rel)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("acceptance: parse %s: %w", rel, err)
	}
	return nil
}

// Parse returns the named Go file's syntax tree, comments included.
func (t *Tree) Parse(rel string) (*ast.File, error) {
	if f, ok := t.asts[rel]; ok {
		return f, nil
	}
	raw, err := t.Read(rel)
	if err != nil {
		return nil, err
	}
	f, err := parser.ParseFile(t.fset, rel, raw, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("acceptance: parse %s: %w", rel, err)
	}
	t.asts[rel] = f
	return f, nil
}

// Dirs returns every directory holding at least one file matching keep,
// sorted.
func (t *Tree) Dirs(keep func(rel string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range t.paths {
		if !keep(p) {
			continue
		}
		dir := path.Dir(p)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	slices.Sort(out)
	return out
}

// Grep returns every file whose text contains needle, sorted, restricted to
// the given extension.
func (t *Tree) Grep(ext, needle string) []string {
	var out []string
	for _, p := range t.WithExt(ext) {
		if strings.Contains(t.Text(p), needle) {
			out = append(out, p)
		}
	}
	return out
}
