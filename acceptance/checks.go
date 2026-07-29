package acceptance

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/johnrichter/claude-shared-tooling/go/schema"
)

// vocabularyFile is this file: the one place the gate spells out the tokens it
// forbids elsewhere. A scan that searched it would find its own vocabulary and
// report the gate as the violation, so every such scan skips it. The cost is
// that a marker planted in this file is invisible to the gate — accepted,
// because the alternative is a gate that can never be green.
const vocabularyFile = "acceptance/checks.go"

// placeholderMarkers are the words an unfinished deliverable leaves behind.
var placeholderMarkers = []string{"TODO", "FIXME", "XXX", "TBD", "PLACEHOLDER", "<placeholder>"}

// interfaceMethods are method names whose contract comes from the interface
// they implement rather than from a doc comment of their own. Requiring prose
// on them would add noise that restates the interface.
var interfaceMethods = []string{"Error", "String", "MarshalJSON", "UnmarshalJSON"}

// widePin is how a node pinned to the wide context window would be written.
const widePin = "[1m]"

// versionControlCall and versionControlProgram together mark a place that
// executes the version-control binary directly rather than going through the
// one package that owns it.
const (
	versionControlCall    = "sysops.Run("
	versionControlProgram = `"git"`
)

// executesVersionControl returns every file outside owner that runs the
// version-control binary itself.
func executesVersionControl(t *Tree, owner string) []string {
	var out []string
	for _, rel := range t.GoSource(false) {
		if rel == vocabularyFile || path.Dir(rel) == owner {
			continue
		}
		for _, line := range strings.Split(t.Text(rel), "\n") {
			if strings.Contains(line, versionControlCall) && strings.Contains(line, versionControlProgram) {
				out = append(out, note("%s: executes the version-control binary outside %s, the one package that owns it", rel, owner))
				break
			}
		}
	}
	return out
}

// validateAgainstTree compiles the contract file at rel from the tree's own
// bytes and validates doc against it.
//
// Compiling from the tree rather than from the copy embedded in this binary is
// what makes the clause a statement about the checkout: an embedded contract
// would report the same answer however the checkout's own files had changed.
func validateAgainstTree(t *Tree, rel string, doc any) ([]clikit.Diagnostic, error) {
	raw, err := t.Read(rel)
	if err != nil {
		return nil, err
	}
	compiled, err := schema.Compile(rel, raw)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", rel, err)
	}
	tree, err := asTree(doc)
	if err != nil {
		return nil, err
	}
	return schema.Validate(compiled, tree)
}

// asTree renders a document as the decoded tree a validator works over,
// accepting either a JSON string or a Go value.
func asTree(doc any) (any, error) {
	raw, ok := doc.(string)
	if !ok {
		encoded, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("encode the probe document: %w", err)
		}
		raw = string(encoded)
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("parse the probe document: %w", err)
	}
	return out, nil
}

// mentions reports whether any violation names the given member, wherever it
// records it — in the message or in the location it points at.
func mentions(diags []clikit.Diagnostic, member string) bool {
	for _, d := range diags {
		raw, err := json.Marshal(d)
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), member) {
			return true
		}
	}
	return false
}

// note formats one violation message.
func note(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// undocumented returns every exported package-level symbol in the tree with no
// doc comment, and every package with no package comment.
//
// A method counts as part of the API only when its receiver type is exported:
// a method on an unexported type is reachable from nowhere outside its
// package, whatever its name looks like.
func undocumented(t *Tree) []string {
	var out []string
	documented := map[string]bool{}
	packages := map[string]bool{}
	for _, rel := range t.GoSource(true) {
		f, err := t.Parse(rel)
		if err != nil {
			out = append(out, note("%s: does not parse: %v", rel, err))
			continue
		}
		dir := path.Dir(rel)
		packages[dir] = true
		if f.Doc != nil {
			documented[dir] = true
		}
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		out = append(out, undocumentedDecls(rel, f)...)
	}
	for dir := range packages {
		if !documented[dir] {
			out = append(out, note("%s: the package has no package comment", dir))
		}
	}
	slices.Sort(out)
	return out
}

// undocumentedDecls returns one message per undocumented exported declaration
// in a single file.
func undocumentedDecls(rel string, f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() || d.Doc != nil {
				continue
			}
			if d.Recv != nil {
				if !exportedReceiver(d.Recv) || slices.Contains(interfaceMethods, d.Name.Name) {
					continue
				}
				out = append(out, note("%s: exported method %s has no doc comment", rel, d.Name.Name))
				continue
			}
			out = append(out, note("%s: exported func %s has no doc comment", rel, d.Name.Name))
		case *ast.GenDecl:
			if d.Tok == token.IMPORT || d.Doc != nil {
				continue
			}
			for _, spec := range d.Specs {
				out = append(out, undocumentedSpec(rel, spec)...)
			}
		}
	}
	return out
}

// undocumentedSpec returns a message when one declaration inside a group is
// exported and carries no doc comment of its own.
func undocumentedSpec(rel string, spec ast.Spec) []string {
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if s.Name.IsExported() && s.Doc == nil {
			return []string{note("%s: exported type %s has no doc comment", rel, s.Name.Name)}
		}
	case *ast.ValueSpec:
		if s.Doc != nil {
			return nil
		}
		var out []string
		for _, n := range s.Names {
			if n.IsExported() {
				out = append(out, note("%s: exported %s has no doc comment", rel, n.Name))
			}
		}
		return out
	}
	return nil
}

// exportedReceiver reports whether a method's receiver type is exported.
func exportedReceiver(recv *ast.FieldList) bool {
	if recv == nil || len(recv.List) == 0 {
		return false
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if index, ok := expr.(*ast.IndexExpr); ok {
		expr = index.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.IsExported()
}

// markdownLink matches an inline Markdown link's target.
var markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// brokenLinks returns every relative Markdown link in the tree that resolves
// to nothing. Absolute URLs and bare anchors are somebody else's to verify.
func brokenLinks(t *Tree) []string {
	var out []string
	for _, rel := range t.WithExt(".md") {
		for _, m := range markdownLink.FindAllStringSubmatch(t.Text(rel), -1) {
			target := m[1]
			if strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			if target == "" {
				continue
			}
			resolved := path.Clean(path.Join(path.Dir(rel), target))
			if t.Has(resolved) || t.Has(path.Join(resolved, "README.md")) || isDir(t, resolved) {
				continue
			}
			out = append(out, note("%s: link to %s resolves to nothing", rel, target))
		}
	}
	slices.Sort(out)
	return out
}

// isDir reports whether the tree holds at least one file under rel.
func isDir(t *Tree, rel string) bool {
	prefix := rel + "/"
	for _, p := range t.Paths() {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// markersIn returns every file of the given extension carrying a placeholder
// marker, excluding the file that declares the marker vocabulary.
func markersIn(t *Tree, ext string) []string {
	var out []string
	for _, rel := range t.WithExt(ext) {
		if rel == vocabularyFile {
			continue
		}
		body := t.Text(rel)
		for _, marker := range placeholderMarkers {
			if strings.Contains(body, marker) {
				out = append(out, note("%s: carries the unfinished-work marker %q", rel, marker))
			}
		}
	}
	slices.Sort(out)
	return out
}

// stringSliceVar returns the string literals of a package-level slice variable
// declared in the named Go file, and whether the variable was found at all.
func stringSliceVar(t *Tree, rel, name string) ([]string, bool) {
	f, err := t.Parse(rel)
	if err != nil {
		return nil, false
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != name || len(value.Values) != 1 {
				continue
			}
			lit, ok := value.Values[0].(*ast.CompositeLit)
			if !ok {
				return nil, false
			}
			var out []string
			for _, el := range lit.Elts {
				basic, ok := el.(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(basic.Value)
				if err != nil {
					continue
				}
				out = append(out, text)
			}
			return out, true
		}
	}
	return nil, false
}

// constStrings returns every package-level string constant declared in the
// named Go file, keyed by its identifier.
func constStrings(t *Tree, rel string) map[string]string {
	out := map[string]string{}
	f, err := t.Parse(rel)
	if err != nil {
		return out
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				basic, ok := value.Values[i].(*ast.BasicLit)
				if !ok || basic.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(basic.Value)
				if err != nil {
					continue
				}
				out[n.Name] = text
			}
		}
	}
	return out
}

// intConsts returns every package-level integer constant declared in the named
// Go file, keyed by its identifier.
func intConsts(t *Tree, rel string) map[string]int {
	out := map[string]int{}
	f, err := t.Parse(rel)
	if err != nil {
		return out
	}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range value.Names {
				if i >= len(value.Values) {
					continue
				}
				basic, ok := value.Values[i].(*ast.BasicLit)
				if !ok || basic.Kind != token.INT {
					continue
				}
				n64, err := strconv.Atoi(basic.Value)
				if err != nil {
					continue
				}
				out[n.Name] = n64
			}
		}
	}
	return out
}

// composites returns every composite literal in the named Go file whose type
// is the given qualified name, as a map from field name to its source text.
func composites(t *Tree, rel, qualified string) []map[string]ast.Expr {
	f, err := t.Parse(rel)
	if err != nil {
		return nil
	}
	var out []map[string]ast.Expr
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || exprName(lit.Type) != qualified {
			return true
		}
		fields := map[string]ast.Expr{}
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			fields[key.Name] = kv.Value
		}
		out = append(out, fields)
		return true
	})
	return out
}

// exprName renders a type expression as its qualified name, empty when it is
// not a plain or package-qualified identifier.
func exprName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			return ""
		}
		return pkg.Name + "." + e.Sel.Name
	}
	return ""
}

// literalText renders a string-literal expression's value, empty for anything
// that is not one.
func literalText(expr ast.Expr) string {
	basic, ok := expr.(*ast.BasicLit)
	if !ok || basic.Kind != token.STRING {
		return ""
	}
	text, err := strconv.Unquote(basic.Value)
	if err != nil {
		return ""
	}
	return text
}

// requirements returns the module paths and versions of go.mod's require
// entries, and the module paths its replace directives cover.
func requirements(t *Tree) (require map[string]string, replace map[string]bool) {
	require, replace = map[string]string{}, map[string]bool{}
	inBlock := false
	for _, line := range strings.Split(t.Text("go.mod"), "\n") {
		trimmed := strings.TrimSpace(line)
		if before, _, found := strings.Cut(trimmed, "//"); found {
			trimmed = strings.TrimSpace(before)
		}
		switch {
		case trimmed == "require (":
			inBlock = true
			continue
		case trimmed == ")":
			inBlock = false
			continue
		case strings.HasPrefix(trimmed, "replace "):
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				replace[fields[1]] = true
			}
			continue
		case strings.HasPrefix(trimmed, "require "):
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 {
				require[fields[1]] = fields[2]
			}
			continue
		}
		if !inBlock {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 {
			require[fields[0]] = fields[1]
		}
	}
	return require, replace
}
