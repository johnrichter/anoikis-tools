package effort

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/johnrichter/anoikis-tools/internal/dag"
	"github.com/johnrichter/anoikis-tools/schemas"
	"github.com/johnrichter/claude-shared-tooling/go/state"
)

// MaxLineBytes bounds one run-log line, including its newline.
//
// A write below this size lands in a single atomic append on every platform
// this runs on, which is what lets a killed process leave a log whose last
// line is either wholly there or wholly absent. An event that would exceed it
// is refused rather than truncated: a half-written transition would be a lie
// about what happened.
const MaxLineBytes = 4096

// TooLargeError reports an event that will not fit in one atomic append.
type TooLargeError struct {
	RunID string
	Size  int
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("effort: run-log event for run %s is %d bytes, max %d; shorten its detail or move the payload to an artifact reference", e.RunID, e.Size, MaxLineBytes)
}

// AppendEvent adds one transition to the run log.
//
// The event is validated against the run-log contract, rendered as one line,
// and appended in a single write to a file opened for append. There is one
// writer — this process — so ordering and atomicity come from that, not from
// a lock. Nothing in the log is ever rewritten.
func (s *Store) AppendEvent(e dag.LogEvent) error {
	e.SchemaVersion = dag.SchemaVersion
	diags, err := schemas.RunLogEvent.Validate(e)
	if err != nil {
		return fmt.Errorf("effort: validate run-log event: %w", err)
	}
	if len(diags) > 0 {
		return &ContractError{Path: s.L.RunLog(), Artifact: schemas.RunLogEvent, Diagnostics: diags}
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("effort: encode run-log event: %w", err)
	}
	line = append(line, '\n')
	if len(line) > MaxLineBytes {
		return &TooLargeError{RunID: e.RunID, Size: len(line)}
	}
	if err := os.MkdirAll(filepath.Dir(s.L.RunLog()), dirPerm); err != nil {
		return fmt.Errorf("effort: create %s: %w", filepath.Dir(s.L.RunLog()), err)
	}
	f, err := os.OpenFile(s.L.RunLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, FilePerm)
	if err != nil {
		return fmt.Errorf("effort: open run log: %w", err)
	}
	defer func() { _ = f.Close() }()
	terminated, err := endsWithNewline(s.L.RunLog())
	if err != nil {
		return err
	}
	if !terminated {
		// A previous process died part-way through an append. Starting the
		// new event on its own line confines that damage to one unreadable
		// line instead of swallowing this event into it. Nothing already
		// written is rewritten.
		line = append([]byte{'\n'}, line...)
	}
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("effort: append run-log event: %w", err)
	}
	return f.Sync()
}

// endsWithNewline reports whether the file at path is empty or terminated,
// which is the only state a clean append may follow.
func endsWithNewline(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("effort: inspect run-log tail: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("effort: inspect run-log tail: %w", err)
	}
	if info.Size() == 0 {
		return true, nil
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
		return false, fmt.Errorf("effort: inspect run-log tail: %w", err)
	}
	return last[0] == '\n', nil
}

// Scan is what a run-log read found.
type Scan struct {
	// Events are every well-formed event read, in file order.
	Events []dag.LogEvent
	// Offset is the byte offset just past the last well-formed event — where
	// the next read may safely resume from.
	Offset int64
	// Damaged counts lines that could not be read as events. A process
	// killed mid-append leaves at most one, at the end.
	Damaged int
	// DamageDetail describes the last damaged line, for the caveat a caller
	// surfaces. Empty when nothing was damaged.
	DamageDetail string
}

// ScanRunLog reads the run log from the given byte offset.
//
// A line that is truncated, unparseable, or does not satisfy the run-log
// contract is counted as damage and skipped — never a fatal read and never a
// silently accepted half-event. A hard kill during an append is exactly this
// case, so a resume must survive it: the events before the damage are
// complete and usable, and the work the damaged line would have described
// simply was not journalled, which resume treats as never dispatched.
func (s *Store) ScanRunLog(from int64) (Scan, error) {
	f, err := os.Open(s.L.RunLog())
	if os.IsNotExist(err) {
		return Scan{}, nil
	}
	if err != nil {
		return Scan{}, fmt.Errorf("effort: open run log: %w", err)
	}
	defer func() { _ = f.Close() }()

	if from > 0 {
		if _, err := f.Seek(from, io.SeekStart); err != nil {
			return Scan{}, fmt.Errorf("effort: seek run log to %d: %w", from, err)
		}
	}

	scan := Scan{Offset: from}
	r := bufio.NewReaderSize(f, MaxLineBytes)
	for {
		line, err := r.ReadBytes('\n')
		complete := err == nil
		if len(line) == 0 && !complete {
			if err == io.EOF {
				return scan, nil
			}
			return scan, fmt.Errorf("effort: read run log: %w", err)
		}
		consumed := int64(len(line))
		if !complete {
			// No trailing newline: the process died mid-append. The bytes
			// are not a transition, and the offset deliberately stops short
			// of them so a later, complete append is read from here.
			scan.Damaged++
			scan.DamageDetail = "final run-log line has no terminator; a run was journalled only partially"
			return scan, nil
		}
		var e dag.LogEvent
		if err := json.Unmarshal(line, &e); err != nil {
			scan.Damaged++
			scan.DamageDetail = fmt.Sprintf("run-log line at offset %d is not valid JSON", scan.Offset)
			scan.Offset += consumed
			continue
		}
		diags, verr := schemas.RunLogEvent.Validate(e)
		if verr != nil {
			return scan, fmt.Errorf("effort: validate run-log line at offset %d: %w", scan.Offset, verr)
		}
		if len(diags) > 0 {
			scan.Damaged++
			scan.DamageDetail = fmt.Sprintf("run-log line at offset %d violates the run-log contract: %s", scan.Offset, diags[0].Message)
			scan.Offset += consumed
			continue
		}
		scan.Events = append(scan.Events, e)
		scan.Offset += consumed
	}
}

// cursorVersion is the resume cursor's own state-file version. It advances
// only when the cursor's shape changes, independently of the artifact
// contracts.
const cursorVersion = 1

// Cursor is how far into the run log has already been folded into the graph's
// own status fields, so a resume reads the tail rather than the whole history.
type Cursor struct {
	// Offset is the byte the unfolded tail begins at.
	Offset int64
	// NextLayer is the sequence number the next dispatched layer takes. It is
	// carried here because the events that would otherwise establish it sit
	// before the offset, and numbering must not restart when the log is
	// sealed.
	NextLayer int
}

// LoadCursor reads the resume cursor, returning a zero cursor when none has
// been written yet. A cursor written by a newer engine is refused rather than
// reinterpreted, so an old binary cannot resume a run it does not understand.
func (s *Store) LoadCursor() (Cursor, error) {
	doc, err := state.Read(s.L.Cursor(), cursorVersion, nil)
	if err != nil {
		return Cursor{}, fmt.Errorf("effort: read resume cursor: %w", err)
	}
	offset, _ := doc["offset"].(float64)
	nextLayer, _ := doc["next_layer"].(float64)
	return Cursor{Offset: int64(offset), NextLayer: int(nextLayer)}, nil
}

// SaveCursor records how far the run log has been folded in.
func (s *Store) SaveCursor(c Cursor) error {
	doc := state.Empty(cursorVersion)
	doc["offset"] = c.Offset
	doc["next_layer"] = c.NextLayer
	doc["updated"] = state.Now()
	if err := os.MkdirAll(filepath.Dir(s.L.Cursor()), dirPerm); err != nil {
		return fmt.Errorf("effort: create %s: %w", filepath.Dir(s.L.Cursor()), err)
	}
	if err := state.Write(s.L.Cursor(), doc, FilePerm); err != nil {
		return fmt.Errorf("effort: write resume cursor: %w", err)
	}
	return nil
}

// Digest is the content fingerprint stored beside a rendered prompt, so a
// replay can prove it is reissuing the same bytes that were dispatched.
func Digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}
