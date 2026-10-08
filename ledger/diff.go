package ledger

import "strings"

// DiffOp marks a line in a diff.
type DiffOp byte

const (
	Equal  DiffOp = ' '
	Delete DiffOp = '-'
	Insert DiffOp = '+'
)

// DiffLine is one line of a line diff.
type DiffLine struct {
	Op   DiffOp
	Text string
}

// maxEdits bounds the work a diff may do; past it, Trail shows the change
// without a line-by-line diff instead of stalling.
const maxEdits = 4000

// SplitLines splits text into lines without their newline characters.
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// DiffLines returns a shortest line diff from a to b (Myers' algorithm).
// ok is false if the files differ too much to diff quickly.
func DiffLines(a, b []string) (lines []DiffLine, ok bool) {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	mid, ok := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	if !ok {
		return nil, false
	}
	out := make([]DiffLine, 0, pre+len(mid)+suf)
	for _, l := range a[:pre] {
		out = append(out, DiffLine{Equal, l})
	}
	out = append(out, mid...)
	for _, l := range a[len(a)-suf:] {
		out = append(out, DiffLine{Equal, l})
	}
	return out, true
}

func myers(a, b []string) ([]DiffLine, bool) {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		out := make([]DiffLine, 0, n+m)
		for _, l := range a {
			out = append(out, DiffLine{Delete, l})
		}
		for _, l := range b {
			out = append(out, DiffLine{Insert, l})
		}
		return out, true
	}
	total := n + m
	off := total + 1
	v := make([]int, 2*total+3)
	// trace[d] holds v[-d-1 .. d+1] as it was at the start of step d.
	var trace [][]int
	done := false
	for d := 0; d <= total && !done; d++ {
		if d > maxEdits {
			return nil, false
		}
		snap := make([]int, 2*d+3)
		copy(snap, v[off-d-1:off+d+2])
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				done = true
				break
			}
		}
	}
	var rev []DiffLine
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		snap := trace[d]
		at := func(k int) int { return snap[k+d+1] }
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, DiffLine{Equal, a[x-1]})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				rev = append(rev, DiffLine{Insert, b[y-1]})
			} else {
				rev = append(rev, DiffLine{Delete, a[x-1]})
			}
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, true
}

// Hunk is a group of nearby changes with surrounding context, as in a unified diff.
type Hunk struct {
	AStart, ALen, BStart, BLen int
	Lines                      []DiffLine
}

// Hunks groups a diff into hunks with ctx lines of context.
func Hunks(dl []DiffLine, ctx int) []Hunk {
	n := len(dl)
	aPos := make([]int, n+1)
	bPos := make([]int, n+1)
	for i, l := range dl {
		aPos[i+1], bPos[i+1] = aPos[i], bPos[i]
		if l.Op != Insert {
			aPos[i+1]++
		}
		if l.Op != Delete {
			bPos[i+1]++
		}
	}
	var hunks []Hunk
	for i := 0; i < n; {
		if dl[i].Op == Equal {
			i++
			continue
		}
		start := max(0, i-ctx)
		end := i
		for {
			for end < n && dl[end].Op != Equal {
				end++
			}
			j := end
			for j < n && dl[j].Op == Equal && j-end < 2*ctx {
				j++
			}
			if j < n && dl[j].Op != Equal {
				end = j
				continue
			}
			break
		}
		stop := min(n, end+ctx)
		hunks = append(hunks, Hunk{
			AStart: aPos[start] + 1, ALen: aPos[stop] - aPos[start],
			BStart: bPos[start] + 1, BLen: bPos[stop] - bPos[start],
			Lines: dl[start:stop],
		})
		i = stop
	}
	return hunks
}

// lineStats counts added and removed lines between two stored versions.
func (s *Store) lineStats(before, after string) (added, removed int, binary bool) {
	var a, b []byte
	var err error
	if before != "" {
		if a, err = s.GetBlob(before); err != nil {
			return 0, 0, false
		}
	}
	if after != "" {
		if b, err = s.GetBlob(after); err != nil {
			return 0, 0, false
		}
	}
	if IsBinary(a) || IsBinary(b) {
		return 0, 0, true
	}
	if len(a)+len(b) > 2<<20 {
		return 0, 0, false
	}
	dl, ok := DiffLines(SplitLines(string(a)), SplitLines(string(b)))
	if !ok {
		return 0, 0, false
	}
	for _, l := range dl {
		switch l.Op {
		case Insert:
			added++
		case Delete:
			removed++
		}
	}
	return added, removed, false
}
