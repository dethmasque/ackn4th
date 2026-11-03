package onboard

import (
	"fmt"
	"strings"
)

// edit is one line of a line-level diff.
type edit struct {
	kind byte // ' ' kept, '-' removed, '+' added
	a, b int  // 0-based line positions in the old and new text
	text string
}

// maxEdits bounds the diff search; past it a file counts as rewritten.
const maxEdits = 1000

// diffLines computes a shortest line edit script from a to b (Myers'
// algorithm). ok is false when the files differ in more than maxEdits lines.
func diffLines(a, b []string) (edits []edit, ok bool) {
	// Common prefix and suffix need no search.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	for i := range pre {
		edits = append(edits, edit{' ', i, i, a[i]})
	}
	mid, ok := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])
	if !ok {
		return nil, false
	}
	for _, e := range mid {
		e.a += pre
		e.b += pre
		edits = append(edits, e)
	}
	for i := range suf {
		ai, bi := len(a)-suf+i, len(b)-suf+i
		edits = append(edits, edit{' ', ai, bi, a[ai]})
	}
	return edits, true
}

func myers(a, b []string) ([]edit, bool) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, true
	}
	off := n + m
	v := make([]int, 2*off+2)
	// trace[d] holds v[-d..d] as it was before round d.
	var trace [][]int
	for d := 0; d <= n+m; d++ {
		if d > maxEdits {
			return nil, false
		}
		trace = append(trace, append([]int(nil), v[off-d:off+d+1]...))
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b), true
			}
		}
	}
	return nil, false
}

func backtrack(trace [][]int, a, b []string) []edit {
	var rev []edit
	x, y := len(a), len(b)
	for d := len(trace) - 1; d >= 0; d-- {
		if d == 0 {
			for x > 0 && y > 0 {
				x, y = x-1, y-1
				rev = append(rev, edit{' ', x, y, a[x]})
			}
			break
		}
		v := trace[d]
		at := func(k int) int { return v[k+d] }
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x, y = x-1, y-1
			rev = append(rev, edit{' ', x, y, a[x]})
		}
		if x == prevX {
			rev = append(rev, edit{'+', x, prevY, b[prevY]})
		} else {
			rev = append(rev, edit{'-', prevX, y, a[prevX]})
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// unified renders edits as unified-diff hunks with ctx lines of context,
// returning the hunks and the added and removed line counts.
func unified(edits []edit, ctx int) (string, int, int) {
	var changed []int
	adds, dels := 0, 0
	for i, e := range edits {
		switch e.kind {
		case '+':
			adds++
			changed = append(changed, i)
		case '-':
			dels++
			changed = append(changed, i)
		}
	}
	var b strings.Builder
	for i := 0; i < len(changed); {
		start := max(0, changed[i]-ctx)
		j := i
		for j+1 < len(changed) && changed[j+1]-changed[j] <= 2*ctx {
			j++
		}
		end := min(len(edits), changed[j]+ctx+1)
		aLen, bLen := 0, 0
		for _, e := range edits[start:end] {
			if e.kind != '+' {
				aLen++
			}
			if e.kind != '-' {
				bLen++
			}
		}
		first := edits[start]
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", first.a+1, aLen, first.b+1, bLen)
		for _, e := range edits[start:end] {
			b.WriteByte(e.kind)
			b.WriteString(e.text)
			b.WriteByte('\n')
		}
		i = j + 1
	}
	return b.String(), adds, dels
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
