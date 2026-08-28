package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iamclancyliang/pi-go/internal/ai"
	"github.com/iamclancyliang/pi-go/internal/tools"
)

// FileFormatVersion is the shape of the records this writes.
//
// Written into every file so a reader can refuse one it does not understand
// rather than misreading it. A file with no version is from before there was
// one; there are none, so an absent version is an error rather than a guess.
const FileFormatVersion = 1

// FileStore keeps a conversation on disk, one JSON record per line.
//
// The format is pi-go's own. ADR-0006 puts the native wire in this
// repository's hands and rules out Pi interoperability, so the goal here is the
// same CAPABILITY — a conversation that outlives its process and can be
// resumed — rather than bytes another program could read. Trying for the
// latter would mean writing this repository's messages into a shape built for a
// different message model, and the file would be readable by neither.
//
// Line-oriented because the file is appended to for the life of a session and
// read once at the start of the next. A document rewritten on every turn would
// make the cost of a long conversation grow with its length, and a crash
// mid-rewrite would take the whole history with it rather than the last line.
type FileStore struct {
	path string

	mu     sync.Mutex
	file   *os.File
	header fileHeader
	now    func() time.Time

	// leaf is where the conversation currently stands. New entries hang from
	// it, and the conversation is the path from the root down to it.
	//
	// Moving it is what makes a branch: continuing from an earlier entry leaves
	// the entries that followed the original one in the file, unreachable from
	// here but not lost — which is the difference between changing your mind
	// and destroying the evidence.
	leaf string
}

// fileHeader is the first line of a session file.
type fileHeader struct {
	Kind      string `json:"kind"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`

	// Dir is where the session was working. Discovery reads it to offer only
	// the sessions belonging to the directory a user is standing in.
	Dir string `json:"dir"`

	// ParentSession is the file this one was branched from, when it was.
	//
	// A fork copies a conversation up to a point into a new file rather than
	// branching inside the old one, so that the conversation being forked FROM
	// is left exactly as it was. This is the only thing that then says where
	// the copy came from.
	ParentSession string `json:"parent_session,omitempty"`
}

// fileRecord is one entry as it appears on disk.
//
// Every record carries its own id and the id of the one before it. The chain is
// not needed to read a conversation back in order — the file already gives
// that — but it means an entry can be referred to, which is what a later
// feature that branches a conversation would need. Written now because adding
// identity to records that already exist means either rewriting history or
// living with two kinds of record forever.
type fileRecord struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	ParentID  string `json:"parent_id,omitempty"`
	Timestamp string `json:"timestamp"`

	Message    *ai.Message       `json:"message,omitempty"`
	Checkpoint *Checkpoint       `json:"checkpoint,omitempty"`
	Overflow   *OverflowAttempt  `json:"overflow,omitempty"`
	Intent     *wireIntent       `json:"intent,omitempty"`
	Settlement *ToolSettlement   `json:"settlement,omitempty"`
	Failure    *OperationFailure `json:"failure,omitempty"`
}

// wireIntent is a ToolIntent with its replay policy written as the word a
// person would read, rather than as the number the enum happens to use.
//
// The number is an implementation detail that renumbering would silently
// change, and a stored "0" that quietly became a different policy would let a
// call be repeated that had been recorded as unsafe.
type wireIntent struct {
	OperationID string `json:"operation_id"`
	CallID      string `json:"call_id"`
	ResultID    string `json:"result_id"`
	Tool        string `json:"tool"`
	ToolVersion string `json:"tool_version,omitempty"`
	Args        string `json:"args"`
	Replay      string `json:"replay"`
}

const (
	kindMessage    = "message"
	kindCheckpoint = "checkpoint"
	kindOverflow   = "overflow"
	kindIntent     = "tool_intent"
	kindSettlement = "tool_settlement"
	kindFailure    = "failure"
	kindHeader     = "session"
)

// OpenFileStore opens or creates the session file at path.
//
// An existing file is opened for appending and its header is read, so resuming
// continues one conversation rather than starting a second inside the same
// file.
func OpenFileStore(path, dir, id string) (*FileStore, error) {
	return openFileStore(path, dir, id, "")
}

// openFileStore is OpenFileStore with the branch origin, which only a fork has.
//
// Taken here rather than set afterwards: the header is written when the file is
// created, so a value assigned to the struct after that never reaches the file
// — and the link back to the conversation this one came from would be lost
// exactly when it is needed.
func openFileStore(path, dir, id, parentSession string) (*FileStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	store := &FileStore{path: path, now: time.Now}

	existing, err := os.Open(path)
	switch {
	case err == nil:
		header, last, readErr := readHeaderAndLast(existing)
		existing.Close()
		if readErr != nil {
			return nil, readErr
		}
		store.header, store.leaf = header, last
	case os.IsNotExist(err):
		store.header = fileHeader{
			Kind:          kindHeader,
			Version:       FileFormatVersion,
			ID:            id,
			Timestamp:     store.now().UTC().Format(time.RFC3339Nano),
			Dir:           dir,
			ParentSession: parentSession,
		}
	default:
		return nil, fmt.Errorf("session: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	store.file = f

	// The header is written only when the file is new, and before anything
	// else: a file whose first line is an entry cannot be told from one whose
	// header failed to write.
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("session: %w", err)
	}
	if info.Size() == 0 {
		line, err := json.Marshal(store.header)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("session: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			f.Close()
			return nil, fmt.Errorf("session: %w", err)
		}
	}
	return store, nil
}

// ID is this session's identity.
func (s *FileStore) ID() string { return s.header.ID }

// Path is where this session is written.
func (s *FileStore) Path() string { return s.path }

// Close releases the file.
func (s *FileStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Append records entries, all or none.
//
// The whole batch is encoded before any of it is written, and a write that
// fails partway truncates back to where it started. Without that, a torn write
// leaves a half-line the next reader cannot parse — and a conversation that
// cannot be read is worse than one that is missing its last turn, because the
// failure arrives at the start of the NEXT session rather than at the end of
// this one.
func (s *FileStore) Append(ctx context.Context, entries ...Entry) error {
	if len(entries) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return fmt.Errorf("session: the store is closed")
	}

	parent := s.leaf
	var buf []byte
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		record, err := s.encode(e, parent)
		if err != nil {
			return err
		}
		line, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("session: %w", err)
		}
		buf = append(append(buf, line...), '\n')
		parent = record.ID
		ids = append(ids, record.ID)
	}

	before, err := s.file.Seek(0, 1)
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if _, err := s.file.Write(buf); err != nil {
		// Back to where this started. A failure here is reported alongside the
		// original, because a store that could not undo a partial write holds
		// something a reader must be warned about.
		if undo := s.file.Truncate(before); undo != nil {
			return fmt.Errorf("session: %w (and the partial write could not be undone: %v)", err, undo)
		}
		return fmt.Errorf("session: %w", err)
	}
	s.leaf = ids[len(ids)-1]
	return nil
}

// Load returns the conversation as it now stands: the path from the root down
// to the current position, oldest first.
//
// Not the whole file. Entries on branches that were left behind are still
// recorded, and are still reachable through Tree, but they are not what the
// next turn follows from.
func (s *FileStore) Load(ctx context.Context) ([]Entry, error) {
	s.mu.Lock()
	leaf := s.leaf
	s.mu.Unlock()
	return s.branch(ctx, leaf)
}

func (s *FileStore) branch(ctx context.Context, leaf string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	path, err := pathTo(records, leaf)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(path))
	for _, r := range path {
		entry, err := r.decode()
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// records reads every record in the file, in the order they were written.
func (s *FileStore) records() ([]fileRecord, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: %w", err)
	}
	defer f.Close()
	_, records, err := readAll(f)
	return records, err
}

// pathTo walks from a leaf up to the root and returns the result oldest first.
//
// An unknown leaf is an error rather than a silent fall back to the last entry:
// asking to stand at an entry that is not there means something upstream is
// wrong, and answering with a different conversation hides it.
func pathTo(records []fileRecord, leaf string) ([]fileRecord, error) {
	if len(records) == 0 {
		return nil, nil
	}
	byID := make(map[string]fileRecord, len(records))
	for _, r := range records {
		byID[r.ID] = r
	}
	if leaf == "" {
		leaf = records[len(records)-1].ID
	}
	current, found := byID[leaf]
	if !found {
		return nil, fmt.Errorf("session: no entry %q in this conversation", leaf)
	}

	var reversed []fileRecord
	// Bounded by the number of records: a parent chain that pointed back into
	// itself would otherwise walk forever, and a corrupt file must fail rather
	// than hang.
	for range records {
		reversed = append(reversed, current)
		if current.ParentID == "" {
			break
		}
		next, found := byID[current.ParentID]
		if !found {
			// A parent that is not in the file. The chain ends here rather than
			// failing: the entries above it are gone, and what remains is still
			// a conversation, which is more use than none.
			break
		}
		current = next
	}
	path := make([]fileRecord, len(reversed))
	for i, r := range reversed {
		path[len(reversed)-1-i] = r
	}
	return path, nil
}

// Leaf is where the conversation currently stands.
func (s *FileStore) Leaf() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaf
}

// MoveTo puts the conversation back at an earlier entry.
//
// What followed that entry stays in the file. Continuing from here writes a new
// line of conversation beside the old one rather than over it, which is what
// makes changing your mind recoverable.
func (s *FileStore) MoveTo(ctx context.Context, id string) error {
	records, err := s.records()
	if err != nil {
		return err
	}
	if _, err := pathTo(records, id); err != nil {
		return err
	}
	s.mu.Lock()
	s.leaf = id
	s.mu.Unlock()
	return nil
}

// Node is one entry in the shape of the conversation.
type Node struct {
	ID       string
	ParentID string
	Kind     string
	At       time.Time

	// Summary is a short description of what this entry is, for a person
	// choosing among them.
	Summary string

	// OnPath says this entry is part of the conversation as it now stands.
	OnPath bool

	// IsLeaf says nothing follows this entry, which is where a branch can be
	// picked up again.
	IsLeaf bool
}

// Tree describes the whole conversation, branches included.
//
// Returned as a flat list in the order the entries were written rather than as
// a nested structure: the caller showing it needs to say which entries are on
// the current path and which are not, and a flat list with parents named does
// that without anyone having to walk a tree to render one.
func (s *FileStore) Tree(ctx context.Context) ([]Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	path, err := pathTo(records, s.Leaf())
	if err != nil {
		return nil, err
	}
	onPath := make(map[string]bool, len(path))
	for _, r := range path {
		onPath[r.ID] = true
	}
	hasChild := make(map[string]bool, len(records))
	for _, r := range records {
		if r.ParentID != "" {
			hasChild[r.ParentID] = true
		}
	}

	nodes := make([]Node, 0, len(records))
	for _, r := range records {
		at, _ := time.Parse(time.RFC3339Nano, r.Timestamp)
		nodes = append(nodes, Node{
			ID:       r.ID,
			ParentID: r.ParentID,
			Kind:     r.Kind,
			At:       at,
			Summary:  r.summary(),
			OnPath:   onPath[r.ID],
			IsLeaf:   !hasChild[r.ID],
		})
	}
	return nodes, nil
}

// summary is how one entry reads in a listing.
func (r fileRecord) summary() string {
	switch {
	case r.Message != nil:
		text := strings.TrimSpace(r.Message.Content)
		if text == "" && len(r.Message.ToolCalls) > 0 {
			return string(r.Message.Role) + ": calling " + r.Message.ToolCalls[0].Name
		}
		return string(r.Message.Role) + ": " + firstLine(text)
	case r.Checkpoint != nil:
		return "compacted: " + firstLine(r.Checkpoint.Summary)
	case r.Intent != nil:
		return "tool: " + r.Intent.Tool
	case r.Settlement != nil:
		return "tool result"
	case r.Failure != nil:
		return "failed: " + r.Failure.Code
	case r.Overflow != nil:
		return "overflow"
	default:
		return r.Kind
	}
}

func firstLine(s string) string {
	if at := strings.IndexByte(s, '\n'); at >= 0 {
		s = s[:at]
	}
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

// BranchInto copies this conversation, up to the given entry, into a new file.
//
// A copy rather than a branch in place, so the conversation being left is
// untouched — someone who forks to try something else must be able to go back
// to a session that looks exactly as they left it. The new file records where
// it came from, which is the only thing that then connects the two.
//
// The copied entries are re-chained as one line. They came from a single path
// already, but their recorded parents point at ids that will not all be in the
// new file, and a parent that is not there is a chain that ends early.
func (s *FileStore) BranchInto(ctx context.Context, path, dir, id, leaf string) (*FileStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	kept, err := pathTo(records, leaf)
	if err != nil {
		return nil, err
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("session: there is nothing recorded to branch from yet")
	}

	branched, err := openFileStore(path, dir, id, s.path)
	if err != nil {
		return nil, err
	}

	entries := make([]Entry, 0, len(kept))
	for _, r := range kept {
		entry, err := r.decode()
		if err != nil {
			branched.Close()
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := branched.Append(ctx, entries...); err != nil {
		branched.Close()
		return nil, err
	}
	return branched, nil
}

// ParentSession is the file this conversation was branched from, if any.
func (s *FileStore) ParentSession() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header.ParentSession
}

func (s *FileStore) encode(e Entry, parent string) (fileRecord, error) {
	record := fileRecord{
		ID:        newEntryID(s.now()),
		ParentID:  parent,
		Timestamp: s.now().UTC().Format(time.RFC3339Nano),
	}
	switch {
	case e.Message != nil:
		record.Kind, record.Message = kindMessage, e.Message
	case e.Checkpoint != nil:
		record.Kind, record.Checkpoint = kindCheckpoint, e.Checkpoint
	case e.Overflow != nil:
		record.Kind, record.Overflow = kindOverflow, e.Overflow
	case e.Intent != nil:
		record.Kind = kindIntent
		record.Intent = &wireIntent{
			OperationID: e.Intent.OperationID,
			CallID:      e.Intent.CallID,
			ResultID:    e.Intent.ResultID,
			Tool:        e.Intent.Tool,
			ToolVersion: e.Intent.ToolVersion,
			Args:        e.Intent.Args,
			Replay:      e.Intent.Replay.String(),
		}
	case e.Settlement != nil:
		record.Kind, record.Settlement = kindSettlement, e.Settlement
	case e.Failure != nil:
		record.Kind, record.Failure = kindFailure, e.Failure
	default:
		// Exactly one field is set, by the type's own contract. An empty entry
		// is a caller's mistake, and writing it would put a record in the file
		// that says nothing happened.
		return fileRecord{}, fmt.Errorf("session: an entry with nothing in it cannot be recorded")
	}
	return record, nil
}

func (r fileRecord) decode() (Entry, error) {
	switch r.Kind {
	case kindMessage:
		return Entry{Message: r.Message}, nil
	case kindCheckpoint:
		return Entry{Checkpoint: r.Checkpoint}, nil
	case kindOverflow:
		return Entry{Overflow: r.Overflow}, nil
	case kindIntent:
		if r.Intent == nil {
			return Entry{}, fmt.Errorf("session: a %s record carries no intent", r.Kind)
		}
		replay := tools.ReplayNever
		if r.Intent.Replay == tools.ReplaySafe.String() {
			replay = tools.ReplaySafe
		}
		return Entry{Intent: &ToolIntent{
			OperationID: r.Intent.OperationID,
			CallID:      r.Intent.CallID,
			ResultID:    r.Intent.ResultID,
			Tool:        r.Intent.Tool,
			ToolVersion: r.Intent.ToolVersion,
			Args:        r.Intent.Args,
			Replay:      replay,
		}}, nil
	case kindSettlement:
		return Entry{Settlement: r.Settlement}, nil
	case kindFailure:
		return Entry{Failure: r.Failure}, nil
	default:
		// Refused rather than skipped. A record this build does not understand
		// may be the tool call that changed a file, and silently reading past
		// it hands the model a conversation that never happened.
		return Entry{}, fmt.Errorf("session: unknown record kind %q", r.Kind)
	}
}

func readAll(f *os.File) (fileHeader, []fileRecord, error) {
	var header fileHeader
	var records []fileRecord

	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for lines.Scan() {
		raw := lines.Bytes()
		if len(raw) == 0 {
			continue
		}
		if first {
			first = false
			if err := json.Unmarshal(raw, &header); err != nil {
				return header, nil, fmt.Errorf("session: unreadable header: %w", err)
			}
			if header.Kind != kindHeader {
				return header, nil, fmt.Errorf("session: the file does not start with a session header")
			}
			if header.Version != FileFormatVersion {
				return header, nil, fmt.Errorf(
					"session: this file is version %d and this build writes %d",
					header.Version, FileFormatVersion)
			}
			continue
		}
		var record fileRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return header, nil, fmt.Errorf("session: unreadable record: %w", err)
		}
		// Decoded here as well as by the caller, so a record this build cannot
		// read fails when the file is read rather than when something later
		// happens to look at that entry.
		if _, err := record.decode(); err != nil {
			return header, nil, err
		}
		records = append(records, record)
	}
	if err := lines.Err(); err != nil {
		return header, nil, fmt.Errorf("session: %w", err)
	}
	if first {
		return header, nil, fmt.Errorf("session: the file is empty")
	}
	return header, records, nil
}

func readHeaderAndLast(f *os.File) (fileHeader, string, error) {
	var header fileHeader
	var last string

	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	first := true
	for lines.Scan() {
		raw := lines.Bytes()
		if len(raw) == 0 {
			continue
		}
		if first {
			first = false
			if err := json.Unmarshal(raw, &header); err != nil {
				return header, "", fmt.Errorf("session: unreadable header: %w", err)
			}
			if header.Version != FileFormatVersion {
				return header, "", fmt.Errorf(
					"session: this file is version %d and this build writes %d",
					header.Version, FileFormatVersion)
			}
			continue
		}
		var record struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return header, "", fmt.Errorf("session: unreadable record: %w", err)
		}
		last = record.ID
	}
	if err := lines.Err(); err != nil {
		return header, "", fmt.Errorf("session: %w", err)
	}
	return header, last, nil
}
