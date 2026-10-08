package ledger

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func note(s *Store, t *testing.T, text string) *Entry {
	t.Helper()
	e, err := s.Append(Body{Actor: Actor{Kind: "agent", Name: "test"}, Action: Action{Type: "external", Summary: text}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestChainVerifies(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 5; i++ {
		note(s, t, "entry")
	}
	r, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || r.Entries != 6 {
		t.Fatalf("want clean 6-entry ledger, got %d entries, problems %v", r.Entries, r.Problems)
	}
	// Reopening keeps the same key and chains onto the same head.
	s2, err := Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	e := note(s2, t, "after reopen")
	if e.Body.Seq != 6 || e.Body.Prev != r.Head.ID {
		t.Fatalf("reopen did not chain: seq %d prev %s", e.Body.Seq, e.Body.Prev)
	}
}

func TestTamperingIsDetected(t *testing.T) {
	cases := map[string]func(lines [][]byte) [][]byte{
		"edit a past entry": func(l [][]byte) [][]byte {
			l[2] = bytes.Replace(l[2], []byte("second"), []byte("SECOND"), 1)
			return l
		},
		"delete a past entry": func(l [][]byte) [][]byte {
			return append(l[:2:2], l[3:]...)
		},
		"reorder entries": func(l [][]byte) [][]byte {
			l[1], l[2] = l[2], l[1]
			return l
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			note(s, t, "first")
			note(s, t, "second")
			note(s, t, "third")
			raw, _ := os.ReadFile(s.ledgerPath())
			lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
			lines = mutate(lines)
			os.WriteFile(s.ledgerPath(), append(bytes.Join(lines, []byte("\n")), '\n'), 0o600)
			r, err := s.Verify(false)
			if err != nil {
				t.Fatal(err)
			}
			if r.OK() {
				t.Fatal("tampering went unnoticed")
			}
		})
	}
}

func TestForgedSignatureIsDetected(t *testing.T) {
	s := newStore(t)
	note(s, t, "real")
	// A second device appending with its own key cannot pass as this one.
	other := newStore(t)
	other.Dir = s.Dir
	other.priv, other.pub = newStore(t).priv, newStore(t).pub
	if _, err := other.Append(Body{Actor: Actor{Kind: "agent", Name: "evil"}, Action: Action{Type: "external", Summary: "forged"}}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Verify(false)
	if r.OK() {
		t.Fatal("forged entry passed verification")
	}
}

func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	write(t, dir, "keep.txt", "unchanged\n")
	write(t, dir, "edit.txt", "one\ntwo\nthree\n")
	write(t, dir, "gone.txt", "delete me\n")
	return dir
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestSnapshotUndoRedo(t *testing.T) {
	s := newStore(t)
	dir := project(t)
	root, ok := FindRoot(filepath.Join(dir))
	if !ok || root != dir {
		t.Fatalf("FindRoot = %s %v", root, ok)
	}
	p := s.BeginTree(root, true)
	write(t, dir, "edit.txt", "one\nTWO\nthree\nfour\n")
	write(t, dir, "sub/new.txt", "hello\n")
	os.Remove(filepath.Join(dir, "gone.txt"))
	write(t, dir, "node_modules/x.js", "ignored\n")
	changes := s.Finish(p)
	ops := map[string]string{}
	for _, c := range changes {
		rel, _ := filepath.Rel(dir, c.Path)
		ops[filepath.ToSlash(rel)] = c.Op
	}
	want := map[string]string{"edit.txt": "modify", "sub/new.txt": "create", "gone.txt": "delete"}
	if len(ops) != len(want) {
		t.Fatalf("changes = %v", ops)
	}
	for k, v := range want {
		if ops[k] != v {
			t.Fatalf("changes = %v, want %v", ops, want)
		}
	}
	for _, c := range changes {
		if c.Op == "modify" && (c.Added != 2 || c.Removed != 1) {
			t.Fatalf("line stats +%d -%d, want +2 -1", c.Added, c.Removed)
		}
	}
	e, err := s.Append(Body{Actor: Actor{Kind: "agent", Name: "test"}, Action: Action{Type: "tool", Summary: "did things"}, Root: root, Changes: changes})
	if err != nil {
		t.Fatal(err)
	}

	undo, plan, conflicts, err := s.Undo(e, Actor{Kind: "human", Name: "me"}, false, false)
	if err != nil || len(conflicts) > 0 {
		t.Fatalf("undo: %v %v", err, conflicts)
	}
	if len(plan) != 3 {
		t.Fatalf("plan has %d changes", len(plan))
	}
	if read(t, dir, "edit.txt") != "one\ntwo\nthree\n" || read(t, dir, "gone.txt") != "delete me\n" || read(t, dir, "sub/new.txt") != "<missing>" {
		t.Fatal("undo did not restore the folder")
	}
	// Undoing the undo is a redo.
	if _, _, _, err := s.Undo(undo, Actor{Kind: "human", Name: "me"}, false, false); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "edit.txt") != "one\nTWO\nthree\nfour\n" || read(t, dir, "sub/new.txt") != "hello\n" || read(t, dir, "gone.txt") != "<missing>" {
		t.Fatal("redo did not reapply the change")
	}
	if r, _ := s.Verify(true); !r.OK() {
		t.Fatal(r.Problems)
	}
}

func TestUndoRefusesToClobberLaterWork(t *testing.T) {
	s := newStore(t)
	dir := project(t)
	f := filepath.Join(dir, "edit.txt")
	p := s.BeginFiles([]string{f})
	write(t, dir, "edit.txt", "agent version\n")
	e, _ := s.Append(Body{Actor: Actor{Kind: "agent", Name: "test"}, Action: Action{Type: "tool", Summary: "edit"}, Changes: s.Finish(p)})
	write(t, dir, "edit.txt", "my own later work\n")

	_, _, conflicts, err := s.Undo(e, Actor{Kind: "human", Name: "me"}, false, false)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %v %v", conflicts, err)
	}
	if read(t, dir, "edit.txt") != "my own later work\n" {
		t.Fatal("undo overwrote later work without --force")
	}
	if _, _, _, err := s.Undo(e, Actor{Kind: "human", Name: "me"}, true, false); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "edit.txt") != "one\ntwo\nthree\n" {
		t.Fatal("forced undo did not restore")
	}
}

func TestFindRootRefusesHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if _, ok := FindRoot(home); ok {
		t.Fatal("home folder should not be a snapshot root")
	}
}

func TestDiffRoundTrips(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	gen := func() []string {
		n := rng.Intn(40)
		out := make([]string, n)
		for i := range out {
			out[i] = string(rune('a' + rng.Intn(4)))
		}
		return out
	}
	for i := 0; i < 2000; i++ {
		a, b := gen(), gen()
		dl, ok := DiffLines(a, b)
		if !ok {
			t.Fatal("diff gave up on a small input")
		}
		var ra, rb []string
		for _, l := range dl {
			if l.Op != Insert {
				ra = append(ra, l.Text)
			}
			if l.Op != Delete {
				rb = append(rb, l.Text)
			}
		}
		if strings.Join(ra, ",") != strings.Join(a, ",") || strings.Join(rb, ",") != strings.Join(b, ",") {
			t.Fatalf("diff does not reproduce inputs:\na=%v\nb=%v", a, b)
		}
		if lcs := lcsLen(a, b); len(dl) != len(a)+len(b)-lcs {
			t.Fatalf("diff is not minimal: %d lines, want %d", len(dl), len(a)+len(b)-lcs)
		}
	}
}

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else {
				dp[i][j] = max(dp[i-1][j], dp[i][j-1])
			}
		}
	}
	return dp[len(a)][len(b)]
}

func TestHunks(t *testing.T) {
	a := SplitLines("1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n13\n14\n15\n16\n17\n18\n19\n20\n")
	b := append([]string{}, a...)
	b[1] = "two"
	b[17] = "eighteen"
	dl, _ := DiffLines(a, b)
	h := Hunks(dl, 3)
	if len(h) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(h))
	}
	if h[0].AStart != 1 || h[0].ALen != 5 || h[1].AStart != 15 || h[1].ALen != 6 {
		t.Fatalf("hunk ranges: %+v %+v", h[0], h[1])
	}
}
