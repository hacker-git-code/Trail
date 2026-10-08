package ledger

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// HashBytes is the content address of data: lowercase hex SHA-256.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Store) blobPath(h string) string {
	return s.path("objects", h[:2], h[2:])
}

// HasBlob reports whether the object store holds content with hash h.
func (s *Store) HasBlob(h string) bool {
	if len(h) != 64 {
		return false
	}
	_, err := os.Stat(s.blobPath(h))
	return err == nil
}

// PutBlob stores data (once, however many times it is stored) and returns its hash.
func (s *Store) PutBlob(data []byte) (string, error) {
	h := HashBytes(data)
	if s.HasBlob(h) {
		return h, nil
	}
	p := s.blobPath(h)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return h, writeFileAtomic(p, buf.Bytes(), 0o600)
}

// PutFile stores the current content of a file and returns its hash.
func (s *Store) PutFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return s.PutBlob(data)
}

// GetBlob returns stored content, checking it still matches its hash.
func (s *Store) GetBlob(h string) ([]byte, error) {
	if len(h) != 64 {
		return nil, fmt.Errorf("bad object id %q", h)
	}
	raw, err := os.ReadFile(s.blobPath(h))
	if err != nil {
		return nil, fmt.Errorf("object %s is missing", h[:12])
	}
	r, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("object %s is damaged", h[:12])
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("object %s is damaged", h[:12])
	}
	if HashBytes(data) != h {
		return nil, fmt.Errorf("object %s was altered", h[:12])
	}
	return data, nil
}

// IsBinary uses the same rule as git: a NUL byte in the first 8000 bytes.
func IsBinary(b []byte) bool {
	return bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0
}
