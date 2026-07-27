package schemaforward

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// schemaSignatures are the two textual tells a schema-shaped literal leaves in source: the
// "$schema" meta-keyword every canonical schema file in this repo declares, and the
// "properties"/"required" pair every object-typed schema (or fragment of one) declares
// together. Either is enough to flag a file — a hand-transcribed contract is either a full
// standalone schema or an inline fragment of one, and both leave one of these marks.
var schemaSignatures = [][]string{
	{`"$schema"`},
	{`"properties"`, `"required"`},
}

// scannedExt is the file extensions this scan reads as text. A schema literal reaches a
// dispatch payload through code or a rendered prompt, so both source and data files that could
// hold one are in scope.
var scannedExt = map[string]bool{
	".go": true, ".json": true, ".yaml": true, ".yml": true, ".tmpl": true, ".gotmpl": true,
}

// InlineLiteral names one file found carrying a schema-shaped literal outside the canonical
// route.
type InlineLiteral struct {
	Path   string
	Reason string
}

// AssertOnlyRoute walks root and reports every scanned file — outside exemptDirs — that embeds
// a schema-shaped literal (see schemaSignatures). exemptDirs are root-relative directories
// legitimately allowed to carry one: the canonical schema files themselves, and this package's
// own tests, which construct literals to exercise the check.
//
// This is the mechanical half of SC-AGENTCONTRACT's "Forward/Verify are the only route a schema
// takes into a dispatch payload": a file outside exemptDirs matching a signature means some
// other code path is carrying a schema by value instead of by reference to the canonical file —
// exactly the shape of defect FB7 came from. An empty result is the passing verdict; it is
// never inferred from "no candidate files found" — Walk returns an error rather than silently
// reporting zero findings when root does not exist or a file cannot be read.
func AssertOnlyRoute(root string, exemptDirs ...string) ([]InlineLiteral, error) {
	root = filepath.Clean(root)
	exempt := make([]string, len(exemptDirs))
	for i, d := range exemptDirs {
		exempt[i] = filepath.Clean(filepath.Join(root, d))
	}

	var findings []InlineLiteral
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			for _, e := range exempt {
				if path == e {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !scannedExt[filepath.Ext(path)] {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("schemaforward: read %s: %w", path, readErr)
		}
		text := string(data)
		for _, sig := range schemaSignatures {
			if hasAll(text, sig) {
				findings = append(findings, InlineLiteral{
					Path:   path,
					Reason: fmt.Sprintf("matches schema signature %s", strings.Join(sig, "+")),
				})
				break
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return findings, nil
}

// hasAll reports whether text contains every one of parts.
func hasAll(text string, parts []string) bool {
	for _, p := range parts {
		if !strings.Contains(text, p) {
			return false
		}
	}
	return true
}
