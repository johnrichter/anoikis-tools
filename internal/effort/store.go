package effort

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/schemas"
	"github.com/johnrichter/claude-shared-tooling/go/clikit"
	"github.com/johnrichter/claude-shared-tooling/go/docmirror"
	"github.com/johnrichter/claude-shared-tooling/go/fsx"
	"github.com/johnrichter/claude-shared-tooling/go/jsondoc"
)

// ContractError reports an artifact that violates the contract it declares.
// It carries every violation, so one read names every problem in the file
// rather than the first.
type ContractError struct {
	Path        string
	Artifact    schemas.Artifact
	Diagnostics []clikit.Diagnostic
}

func (e *ContractError) Error() string {
	first := "unspecified violation"
	if len(e.Diagnostics) > 0 {
		first = e.Diagnostics[0].Message
	}
	return fmt.Sprintf("effort: %s violates the %s contract (%d violations; first: %s)", e.Path, e.Artifact, len(e.Diagnostics), first)
}

// Store reads and writes one effort's artifacts.
//
// Every document is validated against its owned schema both after it is read
// and before it is written, so a malformed artifact is refused at the edge
// rather than propagating into a scheduling decision. Writes are atomic; an
// artifact the harness declared a mirror for is written together with that
// mirror or not at all.
type Store struct {
	L       Layout
	mirrors map[string]*template.Template
}

// New returns a store over l. mirrors maps an artifact kind to the Markdown
// template that renders its generated view; a kind with no template simply
// has no mirror.
func New(l Layout, mirrors map[string]*template.Template) *Store {
	return &Store{L: l, mirrors: mirrors}
}

// LoadProject reads the effort manifest.
func (s *Store) LoadProject() (dag.Project, error) {
	var p dag.Project
	err := s.read(s.L.Project(), schemas.Project, &p)
	return p, err
}

// SaveProject writes the effort manifest, stamping the current schema
// version.
func (s *Store) SaveProject(p dag.Project) error {
	p.SchemaVersion = dag.SchemaVersion
	return s.write(s.L.Project(), s.L.ProjectMirror(), "project", schemas.Project, p)
}

// LoadIndex reads the graph index.
func (s *Store) LoadIndex() (dag.Index, error) {
	var i dag.Index
	err := s.read(s.L.Index(), schemas.GraphIndex, &i)
	return i, err
}

// SaveIndex writes the graph index.
func (s *Store) SaveIndex(i dag.Index) error {
	i.SchemaVersion = dag.SchemaVersion
	return s.write(s.L.Index(), "", "graph-index", schemas.GraphIndex, i)
}

// LoadShard reads one gate's shard.
func (s *Store) LoadShard(gateID string) (dag.Shard, error) {
	var sh dag.Shard
	err := s.read(s.L.Shard(gateID), schemas.GraphShard, &sh)
	return sh, err
}

// SaveShard writes one gate's shard. A status flip rewrites this one small
// file and nothing else.
func (s *Store) SaveShard(sh dag.Shard) error {
	sh.SchemaVersion = dag.SchemaVersion
	return s.write(s.L.Shard(sh.GateID), "", "graph-shard", schemas.GraphShard, sh)
}

// LoadGates reads the gate policy.
func (s *Store) LoadGates() (dag.Gates, error) {
	var g dag.Gates
	err := s.read(s.L.Gates(), schemas.Gates, &g)
	return g, err
}

// SaveGates writes the gate policy.
func (s *Store) SaveGates(g dag.Gates) error {
	g.SchemaVersion = dag.SchemaVersion
	return s.write(s.L.Gates(), "", "gates", schemas.Gates, g)
}

// LoadDetail reads a node's detail record, falling back to the archive so a
// closed node stays inspectable after its detail has been moved out of the
// hot path.
func (s *Store) LoadDetail(nodeID string) (dag.Detail, error) {
	var d dag.Detail
	err := s.read(s.L.Detail(nodeID), schemas.Node, &d)
	if errors.Is(err, os.ErrNotExist) {
		return d, s.read(s.L.ArchivedDetail(nodeID), schemas.Node, &d)
	}
	return d, err
}

// SaveDetail writes a node's detail record.
func (s *Store) SaveDetail(d dag.Detail) error {
	d.SchemaVersion = dag.SchemaVersion
	return s.write(s.L.Detail(d.ID), "", "node", schemas.Node, d)
}

// SaveResult writes a node's durable run result and returns its
// effort-relative reference.
func (s *Store) SaveResult(r dag.RunResult) (string, error) {
	r.SchemaVersion = dag.SchemaVersion
	path := s.L.Result(r.NodeID)
	if err := s.write(path, "", "run-result", schemas.RunResult, r); err != nil {
		return "", err
	}
	return s.L.Rel(path), nil
}

// WritePrompt stores a run's rendered prompt verbatim and returns its
// effort-relative reference and content digest. A resume replays from these
// bytes, so they are written before the run is journalled as dispatched and
// are never rewritten afterwards.
func (s *Store) WritePrompt(runID, text string) (ref, digest string, err error) {
	path := s.L.Prompt(runID)
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return "", "", fmt.Errorf("effort: create %s: %w", filepath.Dir(path), err)
	}
	if err := fsx.WriteAtomic(path, []byte(text), FilePerm); err != nil {
		return "", "", err
	}
	return s.L.Rel(path), Digest(text), nil
}

// ReadPrompt returns a stored prompt's bytes.
func (s *Store) ReadPrompt(ref string) (string, error) {
	b, err := os.ReadFile(s.L.Resolve(ref))
	if err != nil {
		return "", fmt.Errorf("effort: read prompt %s: %w", ref, err)
	}
	return string(b), nil
}

// ArchiveNode moves a closed node's detail record out of the hot path by
// rename, never by read-write-delete, so an interrupted archival leaves
// either the original or the archived copy and never a partial one.
func (s *Store) ArchiveNode(nodeID string) (string, error) {
	src := s.L.Detail(nodeID)
	dst := s.L.ArchivedDetail(nodeID)
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return "", fmt.Errorf("effort: create %s: %w", filepath.Dir(dst), err)
	}
	if err := fsx.Move(src, dst); err != nil {
		return "", fmt.Errorf("effort: archive %s: %w", nodeID, err)
	}
	return s.L.Rel(dst), nil
}

// LoadState reads the whole effort: manifest, index, every shard the index
// names, gate policy, and the run log's tail from the resume cursor onward.
//
// Every artifact is validated as it is read, so the state an engine decision
// runs on has already been proven to conform.
func (s *Store) LoadState() (dag.State, error) {
	st, _, err := s.LoadStateScan()
	return st, err
}

// LoadStateScan reads the whole effort and also returns what reading the run
// log found, for a caller that must report damage rather than only survive it.
func (s *Store) LoadStateScan() (dag.State, Scan, error) {
	var st dag.State
	var err error
	if st.Project, err = s.LoadProject(); err != nil {
		return st, Scan{}, err
	}
	if st.Index, err = s.LoadIndex(); err != nil {
		return st, Scan{}, err
	}
	for _, ref := range st.Index.Shards {
		sh, err := s.LoadShard(ref.GateID)
		if err != nil {
			return st, Scan{}, err
		}
		st.Shards = append(st.Shards, sh)
	}
	if st.Gates, err = s.LoadGates(); err != nil {
		return st, Scan{}, err
	}
	cursor, err := s.LoadCursor()
	if err != nil {
		return st, Scan{}, err
	}
	scan, err := s.ScanRunLog(cursor.Offset)
	if err != nil {
		return st, scan, err
	}
	st.Events = scan.Events
	st.LayerFloor = cursor.NextLayer
	return st.FoldLog(), scan, nil
}

// SaveShards writes every shard and rebuilds the index rows from them, so
// the counts an index reports can never disagree with the shards they
// summarise.
func (s *Store) SaveShards(shards []dag.Shard, updated string) error {
	index := dag.Index{SchemaVersion: dag.SchemaVersion, Updated: updated}
	for _, sh := range shards {
		if err := s.SaveShard(sh); err != nil {
			return err
		}
		index.Shards = append(index.Shards, dag.ShardRef{
			GateID: sh.GateID,
			Ref:    s.L.Rel(s.L.Shard(sh.GateID)),
			Counts: sh.Tally(),
		})
	}
	return s.SaveIndex(index)
}

// read loads path, validates it against artifact, and decodes it into out.
func (s *Store) read(path string, artifact schemas.Artifact, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("effort: read %s: %w", path, err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("effort: parse %s: %w", path, err)
	}
	diags, err := artifact.Validate(doc)
	if err != nil {
		return fmt.Errorf("effort: validate %s: %w", path, err)
	}
	if len(diags) > 0 {
		return &ContractError{Path: path, Artifact: artifact, Diagnostics: diags}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("effort: decode %s: %w", path, err)
	}
	return nil
}

// write validates doc against artifact and persists it canonically. When the
// harness declared a mirror for kind, the pair is written together — there is
// no path that updates one without the other.
func (s *Store) write(jsonPath, mirrorPath, kind string, artifact schemas.Artifact, doc any) error {
	diags, err := artifact.Validate(doc)
	if err != nil {
		return fmt.Errorf("effort: validate %s: %w", jsonPath, err)
	}
	if len(diags) > 0 {
		return &ContractError{Path: jsonPath, Artifact: artifact, Diagnostics: diags}
	}
	if err := os.MkdirAll(filepath.Dir(jsonPath), dirPerm); err != nil {
		return fmt.Errorf("effort: create %s: %w", filepath.Dir(jsonPath), err)
	}
	if tmpl := s.mirrors[kind]; tmpl != nil && mirrorPath != "" {
		return docmirror.WritePair(jsonPath, mirrorPath, doc, tmpl, FilePerm)
	}
	canon, err := jsondoc.Canonicalize(doc)
	if err != nil {
		return fmt.Errorf("effort: canonicalize %s: %w", jsonPath, err)
	}
	return fsx.WriteAtomic(jsonPath, append(canon, '\n'), FilePerm)
}
