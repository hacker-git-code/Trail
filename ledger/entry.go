package ledger

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// FormatVersion is the ledger format version this package writes (SPEC.md §8).
const FormatVersion = 1

// Actor is who made a change.
type Actor struct {
	Kind    string `json:"kind"` // agent | human | system
	Name    string `json:"name"`
	Session string `json:"session,omitempty"`
	Host    string `json:"host,omitempty"`
}

// Action is what was done.
type Action struct {
	Type    string `json:"type"` // init | tool | run | undo | external
	Tool    string `json:"tool,omitempty"`
	Summary string `json:"summary"`
	Command string `json:"command,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
	Exit    int    `json:"exit,omitempty"`
	PubKey  string `json:"pubkey,omitempty"`
}

// Change is one file moving from one state to another.
type Change struct {
	Path       string `json:"path"`
	Op         string `json:"op"` // create | modify | delete
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	BeforeMode uint32 `json:"before_mode,omitempty"`
	AfterMode  uint32 `json:"after_mode,omitempty"`
	Added      int    `json:"added,omitempty"`
	Removed    int    `json:"removed,omitempty"`
	Binary     bool   `json:"binary,omitempty"`
}

// Body is the signed content of an entry.
type Body struct {
	V       int      `json:"v"`
	Seq     int64    `json:"seq"`
	Prev    string   `json:"prev"`
	Time    string   `json:"time"`
	Key     string   `json:"key"`
	Actor   Actor    `json:"actor"`
	Action  Action   `json:"action"`
	Why     string   `json:"why,omitempty"`
	Root    string   `json:"root,omitempty"`
	Changes []Change `json:"changes,omitempty"`
	Note    string   `json:"note,omitempty"`
	// Recovery tells a person how to reverse an action Trail can't reverse
	// itself (an email sent, a payment made). Optional.
	Recovery string `json:"recovery,omitempty"`
	// Dest is where data went, for entries about data leaving the computer
	// (e.g. "Anthropic (Claude's model)" or "api.example.com").
	Dest string `json:"dest,omitempty"`
	// Sent lists what left: files and outputs an AI agent sent to its model.
	Sent []Sent `json:"sent,omitempty"`
	// Secrets found in what was sent, or in what a guard blocked.
	Secrets []Secret `json:"secrets,omitempty"`
}

// Entry is one line of the ledger.
type Entry struct {
	ID   string
	Sig  string
	Body Body
	Raw  []byte // the exact body bytes the id was computed from
}

// Short is the 8-character form of the id shown to people.
func (e *Entry) Short() string { return e.ID[:8] }

// When is the entry's time.
func (e *Entry) When() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, e.Body.Time)
	return t
}

// Line is the entry exactly as it appears in ledger.jsonl.
func (e *Entry) Line() []byte {
	var b bytes.Buffer
	b.WriteString(`{"id":"`)
	b.WriteString(e.ID)
	b.WriteString(`","sig":"`)
	b.WriteString(e.Sig)
	b.WriteString(`","body":`)
	b.Write(e.Raw)
	b.WriteString("}")
	return b.Bytes()
}

type wireLine struct {
	ID   string          `json:"id"`
	Sig  string          `json:"sig"`
	Body json.RawMessage `json:"body"`
}

func parseLine(line []byte) (*Entry, error) {
	var w wireLine
	if err := json.Unmarshal(line, &w); err != nil {
		return nil, err
	}
	if len(w.ID) != 64 || len(w.Body) == 0 {
		return nil, errors.New("missing id or body")
	}
	e := &Entry{ID: w.ID, Sig: w.Sig, Raw: []byte(w.Body)}
	if err := json.Unmarshal(w.Body, &e.Body); err != nil {
		return nil, err
	}
	return e, nil
}

func (s *Store) ledgerPath() string { return s.path("ledger.jsonl") }

// Append signs b, chains it to the current head and appends it.
// Seq, Prev, Time, Key and V are always set here, never by the caller.
func (s *Store) Append(b Body) (*Entry, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.appendLocked(b)
}

func (s *Store) appendLocked(b Body) (*Entry, error) {
	last, err := s.lastEntry()
	if err != nil {
		return nil, err
	}
	b.V = FormatVersion
	b.Seq, b.Prev = 0, ""
	if last != nil {
		b.Seq, b.Prev = last.Body.Seq+1, last.ID
	}
	b.Time = time.Now().UTC().Format(time.RFC3339Nano)
	b.Key = s.Fingerprint()
	if b.Actor.Host == "" {
		b.Actor.Host, _ = os.Hostname()
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	e := &Entry{
		ID:   hex.EncodeToString(sum[:]),
		Sig:  base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, sum[:])),
		Body: b,
		Raw:  raw,
	}
	f, err := os.OpenFile(s.ledgerPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(append(e.Line(), '\n')); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return nil, err
	}
	return e, f.Close()
}

// lastEntry reads only the final line, so appending stays fast as the ledger grows.
func (s *Store) lastEntry() (*Entry, error) {
	f, err := os.Open(s.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	pos := fi.Size()
	if pos == 0 {
		return nil, nil
	}
	const chunk = 64 << 10
	var buf []byte
	for {
		start := max(pos-chunk, 0)
		b := make([]byte, pos-start)
		if _, err := f.ReadAt(b, start); err != nil && err != io.EOF {
			return nil, err
		}
		buf = append(b, buf...)
		pos = start
		t := bytes.TrimRight(buf, "\r\n")
		if i := bytes.LastIndexByte(t, '\n'); i >= 0 || pos == 0 {
			if len(bytes.TrimSpace(t)) == 0 {
				return nil, nil
			}
			e, err := parseLine(t[i+1:])
			if err != nil {
				return nil, fmt.Errorf("the last ledger entry is unreadable (%v); run `trail verify`", err)
			}
			return e, nil
		}
	}
}

// Entries reads the whole ledger, oldest first.
func (s *Store) Entries() ([]*Entry, error) {
	f, err := os.Open(s.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	n := 0
	for sc.Scan() {
		n++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		e, err := parseLine(line)
		if err != nil {
			return nil, fmt.Errorf("ledger line %d is unreadable (%v); run `trail verify`", n, err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// EntriesFrom reads the entries that start at byte offset off or later, and
// returns the offset just past the last complete line. A watcher calls it
// repeatedly to see only what was appended since it last looked.
func (s *Store) EntriesFrom(off int64) ([]*Entry, int64, error) {
	f, err := os.Open(s.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, off, nil
	}
	if err != nil {
		return nil, off, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() < off {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, off, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	var out []*Entry
	pos := off
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			pos += int64(len(line))
			if t := bytes.TrimSpace(line); len(t) > 0 {
				if e, perr := parseLine(t); perr == nil {
					out = append(out, e)
				}
			}
		}
		if err != nil {
			break // a line still being written is read next time
		}
	}
	return out, pos, nil
}

// FindIn returns the entry whose id starts with prefix.
func FindIn(entries []*Entry, prefix string) (*Entry, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if len(prefix) < 4 {
		return nil, errors.New("use at least 4 characters of the id")
	}
	var hit *Entry
	for _, e := range entries {
		if strings.HasPrefix(e.ID, prefix) {
			if hit != nil {
				return nil, fmt.Errorf("%q matches more than one entry; use more characters", prefix)
			}
			hit = e
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("no entry starts with %q", prefix)
	}
	return hit, nil
}

// Report is the result of Verify.
type Report struct {
	Entries  int
	Head     *Entry
	Problems []string
}

// OK reports whether verification found nothing wrong.
func (r *Report) OK() bool { return len(r.Problems) == 0 }

// Verify checks every rule in SPEC.md §5. With deep, it also opens every
// object a change refers to and checks its hash.
func (s *Store) Verify(deep bool) (*Report, error) {
	r := &Report{}
	f, err := os.Open(s.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	var pub ed25519.PublicKey
	var prevID string
	line, i := 0, 0
	bad := func(format string, a ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, a...)) }
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		e, err := parseLine(raw)
		if err != nil {
			bad("line %d: unreadable (%v)", line, err)
			prevID = "?"
			i++
			continue
		}
		sum := sha256.Sum256(e.Raw)
		if hex.EncodeToString(sum[:]) != e.ID {
			bad("#%d (%s): content does not match its id — it was edited after it was written", i, e.Short())
		}
		if i == 0 {
			k, err := base64.StdEncoding.DecodeString(e.Body.Action.PubKey)
			if e.Body.Action.Type != "init" || err != nil || len(k) != ed25519.PublicKeySize {
				bad("#0: the first entry is not a valid genesis entry")
			} else {
				pub = ed25519.PublicKey(k)
			}
		}
		if pub != nil {
			sig, err := base64.StdEncoding.DecodeString(e.Sig)
			if err != nil || !ed25519.Verify(pub, sum[:], sig) {
				bad("#%d (%s): signature is not valid", i, e.Short())
			}
		}
		if e.Body.Seq != int64(i) {
			bad("#%d (%s): numbered %d — entries were removed or inserted before it", i, e.Short(), e.Body.Seq)
			i = int(e.Body.Seq) // report a gap once, not for every entry after it
		}
		if e.Body.Prev != prevID && prevID != "?" {
			bad("#%d (%s): does not link to the entry before it — history was reordered or cut", i, e.Short())
		}
		if deep {
			for _, c := range e.Body.Changes {
				for _, h := range []string{c.Before, c.After} {
					if h == "" {
						continue
					}
					if _, err := s.GetBlob(h); err != nil {
						bad("#%d (%s): %v", i, e.Short(), err)
					}
				}
			}
		}
		prevID = e.ID
		r.Head = e
		i++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	r.Entries = i
	if pub != nil && !pub.Equal(s.pub) {
		bad("the ledger was started by a different key than this device's key")
	}
	return r, nil
}
