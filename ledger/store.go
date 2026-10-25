// Package ledger implements the Trail ledger: a signed, append-only,
// hash-chained record of every change made on your behalf, with undo.
// SPEC.md describes the on-disk format this package reads and writes.
package ledger

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config holds local settings. It is not part of the ledger.
type Config struct {
	Wrap          []string `json:"wrap"`            // CLIs wrapped by `trail shell-init`
	MaxFileBytes  int64    `json:"max_file_bytes"`  // larger files are not tracked
	MaxFiles      int      `json:"max_files"`       // larger folders are not snapshotted
	Ignore        []string `json:"ignore"`          // extra ignore patterns, like .trailignore
	Watch         []string `json:"watch"`           // folders `trail watch` keeps a copy of
	WatchMaxFiles int      `json:"watch_max_files"` // larger watched folders are refused
}

func DefaultConfig() Config {
	return Config{Wrap: []string{}, MaxFileBytes: 10 << 20, MaxFiles: 25000, Ignore: []string{}, Watch: []string{}, WatchMaxFiles: 200000}
}

// File is the path of a file in the ledger directory, for local state such as
// the watcher's heartbeat. It is never part of the ledger itself.
func (s *Store) File(name string) string { return s.path(name) }

// Store is an open ledger directory.
type Store struct {
	Dir    string
	Config Config
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
}

// DefaultDir is $TRAIL_HOME, or ~/.trail.
func DefaultDir() (string, error) {
	if d := os.Getenv("TRAIL_HOME"); d != "" {
		return filepath.Abs(d)
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".trail"), nil
}

// Open opens the ledger at dir, creating it (with a fresh key and a genesis
// entry) the first time.
func Open(dir string) (*Store, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"objects", "pending", "sessions", "index"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	s := &Store{Dir: dir, Config: DefaultConfig()}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.loadOrCreateKey(); err != nil {
		return nil, err
	}
	last, err := s.lastEntry()
	if err != nil {
		return nil, err
	}
	if last == nil {
		_, err := s.appendLocked(Body{
			Actor: Actor{Kind: "system", Name: "trail"},
			Action: Action{
				Type:    "init",
				Summary: "trail started",
				PubKey:  base64.StdEncoding.EncodeToString(s.pub),
			},
		})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir}, parts...)...)
}

func (s *Store) loadConfig() error {
	b, err := os.ReadFile(s.path("config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return s.SaveConfig()
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.Config); err != nil {
		return fmt.Errorf("config.json: %w", err)
	}
	d := DefaultConfig()
	if s.Config.MaxFileBytes <= 0 {
		s.Config.MaxFileBytes = d.MaxFileBytes
	}
	if s.Config.MaxFiles <= 0 {
		s.Config.MaxFiles = d.MaxFiles
	}
	if s.Config.WatchMaxFiles <= 0 {
		s.Config.WatchMaxFiles = d.WatchMaxFiles
	}
	if s.Config.Watch == nil {
		s.Config.Watch = []string{}
	}
	if s.Config.Wrap == nil {
		s.Config.Wrap = []string{}
	}
	return nil
}

// SaveConfig writes config.json.
func (s *Store) SaveConfig() error {
	b, err := json.MarshalIndent(s.Config, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path("config.json"), append(b, '\n'), 0o600)
}

func (s *Store) loadOrCreateKey() error {
	b, err := os.ReadFile(s.path("key"))
	if err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("the key file is damaged")
		}
		s.priv = ed25519.NewKeyFromSeed(seed)
		s.pub = s.priv.Public().(ed25519.PublicKey)
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(s.path("key"), []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	if err := writeFileAtomic(s.path("key.pub"), []byte(hex.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return err
	}
	s.priv, s.pub = priv, pub
	return nil
}

// Fingerprint identifies this device's key: the first 16 hex chars of SHA-256(pubkey).
func (s *Store) Fingerprint() string {
	sum := sha256.Sum256(s.pub)
	return hex.EncodeToString(sum[:])[:16]
}

// lock takes the ledger's exclusive lock. It works the same on every OS:
// a lock file created with O_EXCL, considered stale after 30 seconds.
func (s *Store) lock() (func(), error) {
	p := s.path("lock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.WriteString(strconv.Itoa(os.Getpid()))
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) > 30*time.Second {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the ledger is locked by another trail process")
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trail-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error { tmp.Close(); os.Remove(name); return err }
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, perm); err != nil {
		return fail(err)
	}
	if err := os.Rename(name, path); err != nil {
		return fail(err)
	}
	return nil
}

func keyName(k string) string {
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:12])
}

// SetSessionPrompt remembers the latest thing a person asked an agent session
// to do, so the changes that follow can carry that intent as their "why".
func (s *Store) SetSessionPrompt(session, prompt string) error {
	if session == "" {
		return nil
	}
	return writeFileAtomic(s.path("sessions", keyName(session)+".txt"), []byte(prompt), 0o600)
}

// SessionPrompt returns the latest prompt recorded for a session.
func (s *Store) SessionPrompt(session string) string {
	if session == "" {
		return ""
	}
	b, err := os.ReadFile(s.path("sessions", keyName(session)+".txt"))
	if err != nil {
		return ""
	}
	return string(b)
}
