package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/johnrichter/claude-shared-tooling/go/schema"
	yaml "go.yaml.in/yaml/v3"
)

// frontMatter is the one field this package reads out of a document's
// frontmatter block: its tags. Everything else a document declares (name,
// description, id, links, updated, ...) belongs to conventions this package
// has no opinion on.
type frontMatter struct {
	Tags []string `yaml:"tags"`
}

// Gather enumerates every Markdown document directly under dir and reads
// each one's declared type and status tags. A document with no frontmatter,
// or frontmatter with no tags, is still returned — with an empty
// Type/Status — so a caller can report exactly what is missing rather than
// silently skipping it. Non-Markdown entries and subdirectories are not
// documents in this sense and are not candidates.
func Gather(dir string) ([]Candidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("discovery: list %s: %w", dir, err)
	}
	var out []Candidate
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("discovery: read %s: %w", path, err)
		}
		fm, err := parseFrontMatter(raw)
		if err != nil {
			return nil, fmt.Errorf("discovery: parse frontmatter in %s: %w", path, err)
		}
		groups := schema.TagNamespaces(fm.Tags)
		out = append(out, Candidate{
			Path:   path,
			Type:   firstTag(groups["type"]),
			Status: firstTag(groups["status"]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// firstTag returns the first value in a tag namespace's group, or "" when
// the namespace was never declared. A namespace declared more than once
// keeps its first occurrence — the same order TagNamespaces preserves.
func firstTag(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// frontMatterDelim is the line that opens and closes a YAML frontmatter
// block. It is a structural marker of the frontmatter convention itself, not
// a document filename, and is common to every document this package reads.
const frontMatterDelim = "---"

// parseFrontMatter extracts and decodes a document's leading frontmatter
// block. A document with no such block returns a zero frontMatter and no
// error: an undeclared type/status is a normal, reportable observation, not
// a parse failure.
func parseFrontMatter(raw []byte) (frontMatter, error) {
	var fm frontMatter
	body := string(raw)
	if !strings.HasPrefix(body, frontMatterDelim+"\n") {
		return fm, nil
	}
	rest := body[len(frontMatterDelim)+1:]
	end := strings.Index(rest, "\n"+frontMatterDelim)
	if end < 0 {
		return fm, nil
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &fm); err != nil {
		return fm, fmt.Errorf("invalid YAML: %w", err)
	}
	return fm, nil
}
