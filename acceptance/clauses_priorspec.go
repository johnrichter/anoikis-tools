package acceptance

import (
	"encoding/json"
	"go/ast"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/internal/effort"
	"github.com/johnrichter/anoikis-tools/internal/engine"
)

// sharedNamespace is the module namespace the shared libraries live under.
// Every one of them is a sibling repository, never a copy inside this one.
const sharedNamespace = "github.com/johnrichter/claude-shared-tooling/"

// decouplingGuard is the test that keeps this engine free of the harness it
// replaces. Its vocabulary is read from it rather than restated here, so the
// two can never disagree and this gate never has to spell out the tokens it
// forbids.
const decouplingGuard = "decoupling_test.go"

// priorSpecClauses are the acceptance clauses of the plan this engine was
// rebuilt from, restated for a standalone engine: the plan targeted a plugin
// inside a marketplace repository, so a clause about that repository's other
// plugins becomes a clause about this engine owing them nothing.
func priorSpecClauses() []Clause {
	return []Clause{{
		ID:       "prior-spec/production-bar",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "Every code deliverable is production-ready: formatted, documented at every exported symbol and package, and free of unfinished-work markers.",
		Asserts:  "Every Go file in the tree is byte-identical to its formatted form; every package carries a package comment; every exported type, function, constant, variable and method on an exported type carries a doc comment, except methods whose contract comes from the interface they implement; no Go source carries an unfinished-work marker.",
		check:    productionBar,
	}, {
		ID:       "prior-spec/documentation-bar",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "Every documentation deliverable is accurate, with every link resolving and no placeholder left in it.",
		Asserts:  "Every relative link in every Markdown file in the tree resolves to a file or directory that exists; no Markdown file carries an unfinished-work marker.",
		check: func(t *Tree) []string {
			return append(brokenLinks(t), markersIn(t, ".md")...)
		},
	}, {
		ID:       "prior-spec/toolchain-gates",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "The toolchain gates run in continuous integration with zero warnings tolerated, the linter is version-pinned, and no built binary is committed.",
		Asserts:  "The integration workflow runs the formatter check, the vetter, the build, the tests and a linter pinned to an explicit version; the linter's configuration is committed; the no-committed-binaries guard is committed and the workflow runs it; no file in the tree is an executable image.",
		check:    toolchainGates,
	}, {
		ID:       "prior-spec/artifact-contracts",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "Every on-disk artifact conforms to a single canonical contract carrying a semantic-versioned schema version, and the contract files are the one source for each shape.",
		Asserts:  "Every artifact the effort store reads or writes resolves to a canonical contract file that requires a schema_version constrained to a semantic version; every contract file has an exported constant and every constant has a file.",
		check:    artifactContracts,
	}, {
		ID:       "prior-spec/command-contract",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "Every subcommand documents what it does, lists its flags, shows at least one usage example, and emits machine-legible output under a documented exit-code contract.",
		Asserts:  "Every command declares a use line, a short description and a long description; every command's name appears in at least one usage example in the command tree; the exit-code contract is documented; no command writes to standard output except through the single record route.",
		check:    commandContract,
	}, {
		ID:       "prior-spec/dependency-discipline",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "Every third-party capability comes from a pinned, declared dependency; nothing is imported that the module does not require.",
		Asserts:  "Every module requirement carries an explicit version; every placeholder-versioned sibling library is resolved by a replace directive; every non-standard-library import in non-test source belongs to a required module.",
		check:    dependencyDiscipline,
	}, {
		ID:       "prior-spec/deterministic-decisions",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "The scheduling decisions are pure and deterministic: identical state yields an identical directive, so a build is repeatable.",
		Asserts:  "Over every fixture state, two evaluations of the same state — and an evaluation of a separately constructed but equal state — produce byte-identical directives.",
		check:    deterministicDecisions,
	}, {
		ID:       "prior-spec/kill-safe-resume",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "A graceful stop loses no work and a hard kill loses only the work in flight: a resume replays every unfinished run verbatim from what was journalled before it started.",
		Asserts:  "A run whose latest event is a dispatch is reissued, carrying the stored prompt, its digest and the commit its worktree is reset to; a run that finished unmerged is recorded again; a merged run is skipped; a damaged final log line is carried as a caveat rather than failing the resume.",
		check:    killSafeResume,
	}, {
		ID:       "prior-spec/owes-nothing-to-the-prior-harness",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "The harness this engine replaces is untouched by it: nothing here imports it, names it, reads its artifact contracts or bakes in its identifier grammar.",
		Asserts:  "The decoupling guard declares a non-empty vocabulary for each of imports, names and artifact contracts and holds a test for each; re-applying that same vocabulary across every Go and JSON file in the tree, outside the guard itself, finds no match.",
		check:    owesNothingToPriorHarness,
	}, {
		ID:       "prior-spec/shared-libraries-are-imported-not-copied",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "Reusable logic lives once, in the shared libraries, and is imported at a pinned version — never forked into this repository.",
		Asserts:  "At least one shared library is required; every shared-library requirement resolves either to a sibling checkout outside this tree or to a real, independently tagged version; the tree holds no vendored dependency directory, no second module, and no file declaring itself part of the shared namespace.",
		check:    sharedLibrariesImported,
	}, {
		ID:       "prior-spec/plugin-registers-additively",
		Source:   SourcePriorSpec,
		Bar:      BarBuild,
		Requires: "This engine's plugin is additive: it is well-formed, self-contained, and changes nothing that already exists.",
		Asserts:  "The plugin manifest parses and declares a name, a description and a semantic version; every hook command resolves inside the plugin root to a file that exists in the tree; no hook path escapes the plugin root.",
		check:    pluginRegistersAdditively,
	}}
}

// productionBar checks formatting, documentation and unfinished-work markers.
func productionBar(t *Tree) []string {
	var out []string
	for _, rel := range t.GoSource(true) {
		raw, err := t.Read(rel)
		if err != nil {
			out = append(out, note("%s: unreadable: %v", rel, err))
			continue
		}
		formatted, err := format.Source(raw)
		if err != nil {
			out = append(out, note("%s: does not parse: %v", rel, err))
			continue
		}
		if string(formatted) != string(raw) {
			out = append(out, note("%s: is not in its formatted form", rel))
		}
	}
	out = append(out, undocumented(t)...)
	return append(out, markersIn(t, ".go")...)
}

// workflowFile is the continuous-integration workflow every toolchain gate
// must run in.
const workflowFile = ".github/workflows/ci.yml"

// binaryGuard is the committed guard that refuses a built binary in the tree.
const binaryGuard = "release/guard/no-committed-binaries.sh"

// linterVersion matches a pinned linter version in the workflow.
var linterVersion = regexp.MustCompile(`version:\s*v\d+\.\d+\.\d+`)

// executableMagic are the leading bytes of the executable image formats a
// built binary would be committed as.
var executableMagic = [][]byte{
	{0x7f, 'E', 'L', 'F'},
	{0xfe, 0xed, 0xfa, 0xce}, {0xce, 0xfa, 0xed, 0xfe},
	{0xfe, 0xed, 0xfa, 0xcf}, {0xcf, 0xfa, 0xed, 0xfe},
	{'M', 'Z'},
}

// toolchainGates checks the workflow runs every gate, the linter is pinned,
// and nothing built is committed.
func toolchainGates(t *Tree) []string {
	var out []string
	workflow := t.Text(workflowFile)
	if workflow == "" {
		return []string{note("%s: the integration workflow is missing", workflowFile)}
	}
	for name, token := range map[string]string{
		"the formatter check":             "gofmt",
		"the vetter":                      "go vet",
		"the build":                       "go build",
		"the tests":                       "go test",
		"the linter":                      "golangci-lint",
		"the no-committed-binaries guard": path.Base(binaryGuard),
	} {
		if !strings.Contains(workflow, token) {
			out = append(out, note("%s: does not run %s", workflowFile, name))
		}
	}
	if !linterVersion.MatchString(workflow) {
		out = append(out, note("%s: the linter is not pinned to an explicit version", workflowFile))
	}
	if !t.Has(".golangci.yml") && !t.Has(".golangci.yaml") {
		out = append(out, note("the linter configuration is not committed"))
	}
	if !t.Has(binaryGuard) {
		out = append(out, note("%s: the guard is missing", binaryGuard))
	}
	for _, rel := range t.Paths() {
		raw, err := t.Read(rel)
		if err != nil || len(raw) < 4 {
			continue
		}
		for _, magic := range executableMagic {
			if len(raw) >= len(magic) && string(raw[:len(magic)]) == string(magic) {
				out = append(out, note("%s: is a committed executable image", rel))
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// schemaSource is where the artifact contracts are named and compiled.
const schemaSource = "schemas/schemas.go"

// storePackage is the package that reads and writes the on-disk artifacts.
const storePackage = "internal/effort"

// artifactContracts checks every persisted artifact against a canonical
// contract file requiring a semantic-versioned schema version.
func artifactContracts(t *Tree) []string {
	var out []string
	named := constStrings(t, schemaSource)
	if len(named) == 0 {
		return []string{note("%s: declares no artifact contracts", schemaSource)}
	}

	// An artifact is on disk when the store validates against it, which is a
	// fact about the code rather than a list this gate keeps in step.
	reference := make(map[string]*regexp.Regexp, len(named))
	for ident := range named {
		reference[ident] = regexp.MustCompile(`\bschemas\.` + ident + `\b`)
	}
	persisted := map[string]string{}
	for _, rel := range t.GoSource(false) {
		if path.Dir(rel) != storePackage {
			continue
		}
		body := t.Text(rel)
		for ident, artifact := range named {
			if reference[ident].MatchString(body) {
				persisted[ident] = artifact
			}
		}
	}
	if len(persisted) == 0 {
		return []string{note("%s: validates against no artifact contract", storePackage)}
	}

	// The contract is checked by what it refuses, not by what it says: a
	// document with no schema version and one carrying a version that is not
	// semantic must both be turned away.
	for ident, artifact := range persisted {
		rel := "schemas/anoikis/" + artifact + ".schema.json"
		if !t.Has(rel) {
			out = append(out, note("%s: %s has no contract file at %s", schemaSource, ident, rel))
			continue
		}
		for reason, doc := range map[string]string{
			"a document declaring no schema version": `{}`,
			"a schema version that is not semantic":  `{"schema_version": "one"}`,
		} {
			diags, err := validateAgainstTree(t, rel, doc)
			if err != nil {
				out = append(out, note("%s: %v", rel, err))
				continue
			}
			if !mentions(diags, "schema_version") {
				out = append(out, note("%s: accepts %s", rel, reason))
			}
		}
	}

	// A contract file with no constant, or a constant with no file, means the
	// canonical set has drifted from what the engine compiles.
	declared := map[string]bool{}
	for _, artifact := range named {
		declared[artifact] = true
	}
	for _, rel := range t.WithExt(".json") {
		if !strings.HasPrefix(rel, "schemas/anoikis/") {
			continue
		}
		if !declared[strings.TrimSuffix(path.Base(rel), ".schema.json")] {
			out = append(out, note("%s: has no exported constant naming it", rel))
		}
	}
	slices.Sort(out)
	return out
}

// recordRoute is the one call a command's output leaves through.
const recordRoute = "clikit.Emit("

// stdoutWritesOutsideTheRecordRoute returns a violation when a command writes
// to standard output anywhere but as the destination handed to the emitter.
func stdoutWritesOutsideTheRecordRoute(t *Tree, rel string) []string {
	var out []string
	for _, line := range strings.Split(t.Text(rel), "\n") {
		switch {
		case strings.Contains(line, "fmt.Print"):
			out = append(out, note("%s: prints directly instead of emitting one record", rel))
		case strings.Contains(line, "os.Stdout") && !strings.Contains(line, recordRoute):
			out = append(out, note("%s: writes to standard output outside the one record route", rel))
		}
	}
	return out
}

// commandPackage is where the command tree is wired.
const commandPackage = "cmd"

// commandContract checks every command is documented, exemplified and routes
// its output through the one record path.
func commandContract(t *Tree) []string {
	var out []string
	type command struct {
		file, use string
	}
	var commands []command
	exemplified := map[string]bool{}
	for _, rel := range t.GoSource(false) {
		if path.Dir(rel) != commandPackage {
			continue
		}
		for _, fields := range composites(t, rel, "cobra.Command") {
			use := literalText(fields["Use"])
			if use == "" {
				// The root command names itself from the tool constant rather
				// than a literal; it is still a command and still checked.
				use = "(root)"
			}
			commands = append(commands, command{file: rel, use: use})
			for _, required := range []string{"Short", "Long"} {
				if _, ok := fields[required]; !ok {
					out = append(out, note("%s: command %q declares no %s", rel, use, strings.ToLower(required)))
				}
			}
			if expr, ok := fields["Example"]; ok {
				for _, word := range strings.Fields(exampleText(expr)) {
					exemplified[word] = true
				}
			}
		}
		out = append(out, stdoutWritesOutsideTheRecordRoute(t, rel)...)
	}
	if len(commands) == 0 {
		return []string{note("%s: declares no commands", commandPackage)}
	}
	// A name counts as exemplified only when it stands alone as an invocation
	// word, so a short name cannot be satisfied by appearing inside an
	// unrelated flag value or path in some other command's example.
	for _, c := range commands {
		if c.use == "(root)" {
			continue
		}
		name, _, _ := strings.Cut(c.use, " ")
		if !exemplified[name] {
			out = append(out, note("%s: command %q has no usage example anywhere in the command tree", c.file, name))
		}
	}
	if !strings.Contains(t.Text("README.md"), "exit") {
		out = append(out, note("README.md: the exit-code contract is not documented"))
	}
	slices.Sort(out)
	return out
}

// exampleText renders a command's Example field, which is usually a trimmed
// raw string rather than a bare literal.
func exampleText(expr ast.Expr) string {
	if text := literalText(expr); text != "" {
		return text
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return ""
	}
	var parts []string
	for _, arg := range call.Args {
		parts = append(parts, literalText(arg))
	}
	return strings.Join(parts, " ")
}

// placeholderVersion is the version a sibling library carries until it is
// tagged; every one of them must be resolved by a replace directive.
const placeholderVersion = "v0.0.0"

// dependencyDiscipline checks every dependency is declared and pinned.
func dependencyDiscipline(t *Tree) []string {
	var out []string
	require, replace := requirements(t)
	if len(require) == 0 {
		return []string{note("go.mod: declares no requirements")}
	}
	for module, version := range require {
		if !strings.HasPrefix(version, "v") {
			out = append(out, note("go.mod: %s is required at %q, which is not an explicit version", module, version))
		}
		if version == placeholderVersion && !replace[module] {
			out = append(out, note("go.mod: %s carries a placeholder version with no replace directive resolving it", module))
		}
	}
	for _, rel := range t.GoSource(false) {
		f, err := t.Parse(rel)
		if err != nil {
			continue
		}
		for _, spec := range f.Imports {
			imported := literalText(spec.Path)
			if !strings.Contains(strings.SplitN(imported, "/", 2)[0], ".") {
				continue
			}
			if strings.HasPrefix(imported, moduleOf(t)) {
				continue
			}
			if !required(require, imported) {
				out = append(out, note("%s: imports %s, which no module requirement covers", rel, imported))
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// moduleOf returns this module's own path.
func moduleOf(t *Tree) string {
	for _, line := range strings.Split(t.Text("go.mod"), "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// required reports whether some module requirement is a prefix of the import
// path, which is what it means for a dependency to be declared.
func required(require map[string]string, imported string) bool {
	for module := range require {
		if imported == module || strings.HasPrefix(imported, module+"/") {
			return true
		}
	}
	return false
}

// deterministicDecisions checks the same state always yields the same
// directive.
func deterministicDecisions(t *Tree) []string {
	var out []string
	for name, st := range fixtureStates() {
		first, err := stepOf(t, st)
		if err != nil {
			out = append(out, note("%s: %v", name, err))
			continue
		}
		second, err := stepOf(t, st)
		if err != nil {
			out = append(out, note("%s: %v", name, err))
			continue
		}
		rebuilt, err := stepOf(t, fixtureStates()[name])
		if err != nil {
			out = append(out, note("%s: %v", name, err))
			continue
		}
		a, b, c := encode(first), encode(second), encode(rebuilt)
		if a != b {
			out = append(out, note("%s: two evaluations of one state disagree: %s vs %s", name, a, b))
		}
		if a != c {
			out = append(out, note("%s: an equal state built separately yields a different directive: %s vs %s", name, a, c))
		}
	}
	slices.Sort(out)
	return out
}

// encode renders a directive as the bytes a driver would receive.
func encode(d engine.Directive) string {
	raw, err := json.Marshal(d)
	if err != nil {
		return "unencodable: " + err.Error()
	}
	return string(raw)
}

// killSafeResume checks a killed build is picked back up from the log alone.
func killSafeResume(t *Tree) []string {
	st := fixtureState([]dag.Node{
		fixtureNode("a", "svc/a", dag.StatusRunning),
		fixtureNode("b", "svc/b", dag.StatusRunning),
		fixtureNode("c", "svc/c", dag.StatusDone),
	}, dag.GatePending)
	st.Events = []dag.LogEvent{
		{RunID: "r-a", NodeID: "a", Event: dag.EventDispatched, BaseRef: fixtureBase,
			WorktreeRef: "worktrees/a", PromptRef: "prompts/r-a.txt", PromptDigest: effort.Digest("a")},
		{RunID: "r-b", NodeID: "b", Event: dag.EventDispatched},
		{RunID: "r-b", NodeID: "b", Event: dag.EventComplete},
		{RunID: "r-c", NodeID: "c", Event: dag.EventDispatched},
		{RunID: "r-c", NodeID: "c", Event: dag.EventMerged},
	}
	plan := engine.Resume(st, 1, "the final line was journalled only partially", engine.Env{
		Tool: "anoikis", Effort: fixtureEffort,
	})

	var out []string
	want := map[string]engine.ResumeAction{
		"r-a": engine.ResumeReissue,
		"r-b": engine.ResumeRerecord,
		"r-c": engine.ResumeSkip,
	}
	got := map[string]engine.ResumeItem{}
	for _, item := range plan.Items {
		got[item.RunID] = item
	}
	for runID, action := range want {
		item, ok := got[runID]
		if !ok {
			out = append(out, note("run %s is missing from the resume plan", runID))
			continue
		}
		if item.Action != action {
			out = append(out, note("run %s resumes as %q, not %q", runID, item.Action, action))
		}
	}
	if item := got["r-a"]; item.PromptRef == "" || item.PromptDigest == "" || item.BaseRef == "" {
		out = append(out, note("an interrupted run is reissued without the prompt, digest or base commit it was dispatched with: %+v", item))
	}
	if plan.Damaged != 1 || plan.DamageDetail == "" {
		out = append(out, note("a damaged final log line was not carried through as a caveat: damaged=%d detail=%q", plan.Damaged, plan.DamageDetail))
	}
	if len(plan.Commands) == 0 {
		out = append(out, note("the resume plan emits no command to act on"))
	}
	slices.Sort(out)
	return out
}

// owesNothingToPriorHarness re-applies the decoupling guard's own declared
// vocabulary across the tree.
func owesNothingToPriorHarness(t *Tree) []string {
	var out []string
	if !t.Has(decouplingGuard) {
		return []string{note("%s: the decoupling guard is missing", decouplingGuard)}
	}
	for _, test := range []string{
		"TestImportGraphReachesNoPriorHarness",
		"TestNoPriorHarnessNaming",
		"TestNoPriorHarnessArtifactNames",
		"TestNoHardCodedIDGrammar",
	} {
		if !strings.Contains(t.Text(decouplingGuard), "func "+test+"(") {
			out = append(out, note("%s: no longer holds %s", decouplingGuard, test))
		}
	}

	var words, substrings []string
	for _, name := range []string{"foreignNames"} {
		values, ok := stringSliceVar(t, decouplingGuard, name)
		if !ok || len(values) == 0 {
			out = append(out, note("%s: %s declares no vocabulary, so the guard forbids nothing", decouplingGuard, name))
			continue
		}
		words = append(words, values...)
	}
	for _, name := range []string{"foreignImports", "foreignArtifacts"} {
		values, ok := stringSliceVar(t, decouplingGuard, name)
		if !ok || len(values) == 0 {
			out = append(out, note("%s: %s declares no vocabulary, so the guard forbids nothing", decouplingGuard, name))
			continue
		}
		substrings = append(substrings, values...)
	}

	var patterns []*regexp.Regexp
	for _, word := range words {
		patterns = append(patterns, regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])`+regexp.QuoteMeta(word)+`($|[^A-Za-z0-9])`))
	}
	for _, ext := range []string{".go", ".json"} {
		for _, rel := range t.WithExt(ext) {
			if rel == decouplingGuard {
				continue
			}
			body := t.Text(rel)
			for i, re := range patterns {
				if re.MatchString(body) {
					out = append(out, note("%s: carries the name %q, which belongs to the harness this engine replaces", rel, words[i]))
				}
			}
			for _, fragment := range substrings {
				if strings.Contains(body, fragment) {
					out = append(out, note("%s: names %q, which belongs to the harness this engine replaces", rel, fragment))
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// sharedLibrariesImported checks reusable logic is imported, never forked.
func sharedLibrariesImported(t *Tree) []string {
	var out []string
	require, replace := requirements(t)
	shared := 0
	for module, version := range require {
		if !strings.HasPrefix(module, sharedNamespace) {
			continue
		}
		shared++
		switch {
		case replacedOutsideTree(t, module):
			// A monorepo-development stand-in: pinned to a sibling checkout.
		case !replace[module] && version != "v0.0.0":
			// A real, independently tagged release: pinned by version + go.sum.
		default:
			out = append(out, note("go.mod: %s is not resolved to a checkout outside this tree or a tagged version", module))
		}
	}
	if shared == 0 {
		out = append(out, note("go.mod: requires no shared library, so nothing reusable is imported"))
	}
	for _, rel := range t.Paths() {
		switch {
		case strings.HasPrefix(rel, "vendor/"):
			out = append(out, note("%s: a vendored dependency tree is a fork of somebody else's source", rel))
		case rel != "go.mod" && path.Base(rel) == "go.mod":
			out = append(out, note("%s: a second module inside this tree", rel))
		}
	}
	for _, rel := range t.GoSource(true) {
		if strings.Contains(t.Text(rel), "package "+path.Base(sharedNamespace)) {
			out = append(out, note("%s: declares itself part of the shared namespace", rel))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// replacedOutsideTree reports whether a module's replace directive points at a
// path outside this checkout.
func replacedOutsideTree(t *Tree, module string) bool {
	for _, line := range strings.Split(t.Text("go.mod"), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "replace "+module+" ") {
			continue
		}
		_, target, found := strings.Cut(trimmed, "=>")
		if !found {
			return false
		}
		return strings.HasPrefix(strings.TrimSpace(target), "../")
	}
	return false
}

// pluginManifest is the plugin's own declaration.
const pluginManifest = "plugin/.claude-plugin/plugin.json"

// pluginHooks is where the plugin's hooks are registered.
const pluginHooks = "plugin/hooks/hooks.json"

// pluginRoot is the variable a hook command resolves its own directory from.
const pluginRoot = "${CLAUDE_PLUGIN_ROOT}"

// semver matches a semantic version.
var semver = regexp.MustCompile(`^\d+\.\d+\.\d+`)

// pluginRegistersAdditively checks the plugin is well-formed and
// self-contained.
func pluginRegistersAdditively(t *Tree) []string {
	var out []string
	var manifest struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
	}
	if err := t.JSON(pluginManifest, &manifest); err != nil {
		return []string{note("%v", err)}
	}
	if manifest.Name == "" {
		out = append(out, note("%s: declares no name", pluginManifest))
	}
	if manifest.Description == "" {
		out = append(out, note("%s: declares no description", pluginManifest))
	}
	if !semver.MatchString(manifest.Version) {
		out = append(out, note("%s: version %q is not a semantic version", pluginManifest, manifest.Version))
	}

	var hooks struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := t.JSON(pluginHooks, &hooks); err != nil {
		return append(out, note("%v", err))
	}
	if len(hooks.Hooks) == 0 {
		out = append(out, note("%s: registers no hook", pluginHooks))
	}
	for event, matchers := range hooks.Hooks {
		for _, matcher := range matchers {
			for _, hook := range matcher.Hooks {
				out = append(out, hookProblems(t, event, hook.Command)...)
			}
		}
	}
	slices.Sort(out)
	return out
}

// hookProblems checks one hook command resolves inside the plugin root.
func hookProblems(t *Tree, event, command string) []string {
	trimmed := strings.Trim(strings.TrimSpace(command), `"`)
	if !strings.HasPrefix(trimmed, pluginRoot) {
		return []string{note("%s: the %s hook command %q does not resolve from the plugin root", pluginHooks, event, command)}
	}
	rel := path.Join("plugin", strings.TrimPrefix(strings.TrimPrefix(trimmed, pluginRoot), "/"))
	if strings.Contains(rel, "..") {
		return []string{note("%s: the %s hook command %q escapes the plugin root", pluginHooks, event, command)}
	}
	if !t.Has(rel) {
		return []string{note("%s: the %s hook runs %s, which is not in the tree", pluginHooks, event, rel)}
	}
	info, err := os.Stat(filepath.Join(t.Root(), filepath.FromSlash(rel)))
	if err != nil || info.Mode()&0o111 == 0 {
		return []string{note("%s: the %s hook runs %s, which is not executable", pluginHooks, event, rel)}
	}
	return nil
}
