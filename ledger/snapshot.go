package ledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileState is a file at one moment. An empty Hash means the file did not exist.
type FileState struct {
	Hash  string `json:"h,omitempty"`
	Mode  uint32 `json:"m,omitempty"`
	Size  int64  `json:"s,omitempty"`
	MTime int64  `json:"t,omitempty"`
}

// Manifest is every tracked file in a folder, keyed by slash-separated relative path.
type Manifest map[string]FileState

var (
	ErrScopeTooLarge = errors.New("folder too large to snapshot")
	ErrTooBig        = errors.New("file too large to track")
)

// Folders that are rebuilt from elsewhere and would only add noise.
var defaultIgnore = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, ".turbo": true, ".cache": true,
	".trail": true, ".DS_Store": true,
}

func (s *Store) ignorer(root string) func(rel, name string) bool {
	pats := append([]string{}, s.Config.Ignore...)
	if b, err := os.ReadFile(filepath.Join(root, ".trailignore")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				pats = append(pats, l)
			}
		}
	}
	return func(rel, name string) bool {
		if defaultIgnore[name] {
			return true
		}
		for _, p := range pats {
			p = strings.TrimSuffix(p, "/")
			if strings.Contains(p, "/") {
				if ok, _ := path.Match(strings.TrimPrefix(p, "/"), rel); ok {
					return true
				}
			} else if ok, _ := path.Match(p, name); ok {
				return true
			}
		}
		return false
	}
}

func (s *Store) indexPath(root string) string {
	return s.path("index", keyName(root)+".json")
}

func (s *Store) loadIndex(root string) Manifest {
	m := Manifest{}
	if b, err := os.ReadFile(s.indexPath(root)); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// Snapshot records the current state of every tracked file under root,
// storing the content of any version it has not seen before. A per-folder
// index means unchanged files are not re-read.
func (s *Store) Snapshot(root string) (Manifest, error) {
	return s.SnapshotMax(root, s.Config.MaxFiles)
}

// SnapshotMax is Snapshot with its own file limit (watched folders allow more).
func (s *Store) SnapshotMax(root string, maxFiles int) (Manifest, error) {
	ignored := s.ignorer(root)
	idx := s.loadIndex(root)
	out := Manifest{}
	now := time.Now().UnixNano()
	count := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if ignored(rel, d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > s.Config.MaxFileBytes {
			return nil
		}
		if count++; count > maxFiles {
			return ErrScopeTooLarge
		}
		mt := info.ModTime().UnixNano()
		mode := uint32(info.Mode().Perm())
		// Trust the index only for files untouched for 2s, so a write landing in
		// the same clock tick as the last snapshot is never missed.
		if c, ok := idx[rel]; ok && c.Size == info.Size() && c.MTime == mt && now-mt > int64(2*time.Second) && s.HasBlob(c.Hash) {
			c.Mode = mode
			out[rel] = c
			return nil
		}
		h, err := s.PutFile(p)
		if err != nil {
			return nil
		}
		out[rel] = FileState{Hash: h, Mode: mode, Size: info.Size(), MTime: mt}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if b, err := json.Marshal(out); err == nil {
		writeFileAtomic(s.indexPath(root), b, 0o600)
	}
	return out, nil
}

// Measure counts the files under root that Trail would keep a copy of, and
// their total size, stopping early once it passes limit files.
func (s *Store) Measure(root string, limit int) (files int, bytes int64) {
	ignored := s.ignorer(root)
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if ignored(filepath.ToSlash(rel), d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > s.Config.MaxFileBytes {
			return nil
		}
		files++
		bytes += info.Size()
		if files > limit {
			return filepath.SkipAll
		}
		return nil
	})
	return files, bytes
}

// SameManifest reports whether two snapshots describe identical files.
func SameManifest(a, b Manifest) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || x.Hash != y.Hash || x.Mode != y.Mode {
			return false
		}
	}
	return true
}

// StateOf records the current state of one file.
func (s *Store) StateOf(p string) (FileState, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return FileState{}, nil
	}
	if err != nil {
		return FileState{}, err
	}
	if !info.Mode().IsRegular() {
		return FileState{}, fmt.Errorf("%s is not a regular file", p)
	}
	if info.Size() > s.Config.MaxFileBytes {
		return FileState{}, ErrTooBig
	}
	h, err := s.PutFile(p)
	if err != nil {
		return FileState{}, err
	}
	return FileState{Hash: h, Mode: uint32(info.Mode().Perm()), Size: info.Size(), MTime: info.ModTime().UnixNano()}, nil
}

func (s *Store) makeChange(p string, b, a FileState) (Change, bool) {
	if b.Hash == a.Hash && (b.Hash == "" || b.Mode == a.Mode) {
		return Change{}, false
	}
	c := Change{Path: p, Before: b.Hash, After: a.Hash, BeforeMode: b.Mode, AfterMode: a.Mode}
	switch {
	case b.Hash == "":
		c.Op = "create"
	case a.Hash == "":
		c.Op = "delete"
	default:
		c.Op = "modify"
	}
	c.Added, c.Removed, c.Binary = s.lineStats(c.Before, c.After)
	return c, true
}

// DiffManifests lists what changed under root between two snapshots.
func (s *Store) DiffManifests(root string, before, after Manifest) []Change {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []Change
	for _, rel := range sorted {
		if c, ok := s.makeChange(filepath.Join(root, filepath.FromSlash(rel)), before[rel], after[rel]); ok {
			out = append(out, c)
		}
	}
	return out
}

// FindRoot picks the folder an action should be scoped to: the enclosing git
// repository, or dir itself. ok is false when dir is too broad to snapshot
// safely (a home folder or a drive root).
func FindRoot(dir string) (root string, ok bool) {
	dir, _ = filepath.Abs(dir)
	home, _ := os.UserHomeDir()
	if dir == home || filepath.Dir(dir) == dir {
		return dir, false
	}
	for d := dir; d != home; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, true
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir, true
}

// Pending is the "before" half of an action, held until the action finishes.
type Pending struct {
	Kind    string               `json:"kind"` // files | tree | none
	Root    string               `json:"root,omitempty"`
	Files   map[string]FileState `json:"files,omitempty"`
	Tree    Manifest             `json:"tree,omitempty"`
	Note    string               `json:"note,omitempty"`
	Agent   string               `json:"agent,omitempty"`
	Why     string               `json:"why,omitempty"`
	Started string               `json:"started"`
}

// BeginFiles captures specific files before an action.
func (s *Store) BeginFiles(paths []string) *Pending {
	p := &Pending{Kind: "files", Files: map[string]FileState{}, Started: time.Now().UTC().Format(time.RFC3339Nano)}
	for _, f := range paths {
		st, err := s.StateOf(f)
		if err != nil {
			p.Note = fmt.Sprintf("%s: %v", filepath.Base(f), err)
			continue
		}
		p.Files[f] = st
	}
	return p
}

// BeginTree captures a whole folder before an action.
func (s *Store) BeginTree(root string, scoped bool) *Pending {
	p := &Pending{Kind: "tree", Root: root, Started: time.Now().UTC().Format(time.RFC3339Nano)}
	if !scoped {
		p.Kind = "none"
		p.Note = "not snapshotted: it ran from your home folder or a drive root, which is too broad to track"
		return p
	}
	m, err := s.Snapshot(root)
	switch {
	case errors.Is(err, ErrScopeTooLarge):
		p.Kind = "none"
		p.Note = fmt.Sprintf("not snapshotted: more than %d files here (add a .trailignore)", s.Config.MaxFiles)
	case err != nil:
		p.Kind = "none"
		p.Note = "not snapshotted: " + err.Error()
	default:
		p.Tree = m
	}
	return p
}

// Finish captures the "after" half and returns what changed.
func (s *Store) Finish(p *Pending) []Change {
	switch p.Kind {
	case "files":
		var out []Change
		paths := make([]string, 0, len(p.Files))
		for f := range p.Files {
			paths = append(paths, f)
		}
		sort.Strings(paths)
		for _, f := range paths {
			after, err := s.StateOf(f)
			if err != nil {
				continue
			}
			if c, ok := s.makeChange(f, p.Files[f], after); ok {
				out = append(out, c)
			}
		}
		return out
	case "tree":
		after, err := s.Snapshot(p.Root)
		if err != nil {
			p.Note = "could not snapshot afterwards: " + err.Error()
			return nil
		}
		return s.DiffManifests(p.Root, p.Tree, after)
	}
	return nil
}

func (s *Store) pendingPath(key string) string {
	return s.path("pending", keyName(key)+".json")
}

// SavePending holds p under key until TakePending. Old leftovers (from actions
// that never finished, e.g. a denied tool call) are swept after a day.
func (s *Store) SavePending(key string, p *Pending) error {
	sweep(s.path("pending"), 24*time.Hour)
	sweep(s.path("sessions"), 7*24*time.Hour)
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.pendingPath(key), b, 0o600)
}

// TakePending returns and removes what was saved under key, or nil.
func (s *Store) TakePending(key string) (*Pending, error) {
	fp := s.pendingPath(key)
	b, err := os.ReadFile(fp)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	os.Remove(fp)
	var p Pending
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func sweep(dir string, age time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > age {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
