package ui

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

var bannerArt = []string{
	` █████╗  ██████╗██╗  ██╗███╗   ██╗██╗  ██╗████████╗██╗  ██╗`,
	`██╔══██╗██╔════╝██║ ██╔╝████╗  ██║██║  ██║╚══██╔══╝██║  ██║`,
	`███████║██║     █████╔╝ ██╔██╗ ██║███████║   ██║   ███████║`,
	`██╔══██║██║     ██╔═██╗ ██║╚██╗██║╚════██║   ██║   ██╔══██║`,
	`██║  ██║╚██████╗██║  ██╗██║ ╚████║     ██║   ██║   ██║  ██║`,
	`╚═╝  ╚═╝ ╚═════╝╚═╝  ╚═╝╚═╝  ╚═══╝     ╚═╝   ╚═╝   ╚═╝  ╚═╝`,
}

const tagline = ")) <==> ((   back and forth, forever"

// glyphs used for scrambles and rain: half-width katakana, digits, symbols.
var glyphs = []rune("ｱｲｳｴｵｶｷｸｹｺｻｼｽｾｿﾀﾁﾂﾃﾄﾅﾆﾇﾈﾉﾊﾋﾌﾍﾎﾏﾐﾑﾒﾓﾔﾕﾖﾗﾘﾙﾚﾛﾜﾝ0123456789<>=*+:.¦|")

func randGlyph() rune { return glyphs[rand.IntN(len(glyphs))] }

// Banner prints the ackn4th logo. On an interactive terminal with effects
// enabled the logo decodes itself out of noise first.
func (t *Term) Banner() {
	if t.Width < len([]rune(bannerArt[0]))+2 {
		t.Println(t.Bold(t.Lime("ACKN4TH")), "  ", t.Moss(tagline))
		return
	}
	shades := []func(string) string{t.Lime, t.Lime, t.Green, t.Green, t.Moss, t.Moss}
	if t.Effects {
		t.decode(bannerArt, shades, 26, 28*time.Millisecond)
	} else {
		for i, l := range bannerArt {
			t.Println(shades[i](l))
		}
	}
	t.Println(t.Moss(strings.Repeat(" ", (len([]rune(bannerArt[0]))-len(tagline))/2)), t.Gray(tagline))
	t.Println()
}

// decode reveals lines out of random glyphs, locking characters in from left
// to right with jitter, over the given number of frames.
func (t *Term) decode(lines []string, shades []func(string) string, frames int, delay time.Duration) {
	runes := make([][]rune, len(lines))
	lockAt := make([][]int, len(lines))
	for i, l := range lines {
		runes[i] = []rune(l)
		lockAt[i] = make([]int, len(runes[i]))
		for j := range runes[i] {
			base := j * frames / (len(runes[i]) + 1)
			lockAt[i][j] = base + rand.IntN(frames/3+1)
		}
	}
	for f := 0; f <= frames+frames/3; f++ {
		if f > 0 {
			fmt.Fprintf(t.W, "\x1b[%dA", len(lines))
		}
		for i := range lines {
			var b strings.Builder
			for j, r := range runes[i] {
				if r == ' ' || f >= lockAt[i][j] {
					b.WriteRune(r)
				} else {
					b.WriteRune(randGlyph())
				}
			}
			fmt.Fprint(t.W, "\r\x1b[2K", shades[i](b.String()), "\n")
		}
		time.Sleep(delay)
	}
}

// Decode prints a single line that resolves out of noise. Without effects it
// just prints the line.
func (t *Term) Decode(line string, style func(string) string) {
	if style == nil {
		style = func(s string) string { return s }
	}
	if !t.Effects {
		t.Println(style(line))
		return
	}
	t.decode([]string{line}, []func(string) string{style}, 14, 22*time.Millisecond)
}

// Rain plays a short burst of digital rain in place, then erases it.
func (t *Term) Rain(rows int, d time.Duration) {
	if !t.Effects {
		return
	}
	cols := t.Width - 1
	heads := make([]int, cols)
	speed := make([]int, cols)
	for i := range heads {
		heads[i] = -rand.IntN(rows * 2)
		speed[i] = 1 + rand.IntN(2)
	}
	grid := make([][]rune, rows)
	for i := range grid {
		grid[i] = []rune(strings.Repeat(" ", cols))
	}
	fmt.Fprint(t.W, strings.Repeat("\n", rows), "\x1b[?25l")
	defer fmt.Fprint(t.W, "\x1b[?25h")
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		fmt.Fprintf(t.W, "\x1b[%dA", rows)
		for c := range heads {
			heads[c] += speed[c]
			if heads[c]-rows > rows {
				heads[c] = -rand.IntN(rows)
			}
		}
		for r := 0; r < rows; r++ {
			var b strings.Builder
			for c := 0; c < cols; c++ {
				dist := heads[c] - r
				switch {
				case dist == 0:
					b.WriteString(t.Bold(t.Lime(string(randGlyph()))))
				case dist > 0 && dist < 6:
					b.WriteString(t.Green(string(randGlyph())))
				case dist >= 6 && dist < 12:
					b.WriteString(t.Moss(string(randGlyph())))
				default:
					b.WriteByte(' ')
				}
			}
			fmt.Fprint(t.W, "\r", b.String(), "\n")
		}
		time.Sleep(45 * time.Millisecond)
	}
	fmt.Fprintf(t.W, "\x1b[%dA", rows)
	for r := 0; r < rows; r++ {
		fmt.Fprint(t.W, "\r\x1b[2K\n")
	}
	fmt.Fprintf(t.W, "\x1b[%dA", rows)
}

// PingPong is a one-line activity indicator: a packet bouncing between two
// endpoints while an agent works.
//
//	claude )) ─────●──────── (( gemini   thinking · 12s
type PingPong struct {
	t     *Term
	mu    sync.Mutex
	stop  chan struct{}
	done  chan struct{}
	left  string
	right string
	label string
	start time.Time
}

// NewPingPong returns an idle indicator. It draws nothing unless effects are on.
func (t *Term) NewPingPong() *PingPong { return &PingPong{t: t} }

// Start begins animating between the left and right endpoint names.
func (p *PingPong) Start(left, right, label string) {
	if !p.t.Effects {
		return
	}
	p.Stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.left, p.right, p.label = left, right, label
	p.start = time.Now()
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	go p.loop(p.stop, p.done)
}

// Resume restarts a stopped indicator without resetting its clock, so
// output can be printed between frames.
func (p *PingPong) Resume() {
	if !p.t.Effects {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil || p.start.IsZero() {
		return
	}
	p.stop = make(chan struct{})
	p.done = make(chan struct{})
	go p.loop(p.stop, p.done)
}

// SetLabel updates the status text while running.
func (p *PingPong) SetLabel(label string) {
	p.mu.Lock()
	p.label = label
	p.mu.Unlock()
}

// Stop halts the animation and clears its line. Safe to call when idle.
func (p *PingPong) Stop() {
	p.mu.Lock()
	stop, done := p.stop, p.done
	p.stop, p.done = nil, nil
	p.mu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
	fmt.Fprint(p.t.W, "\r\x1b[2K")
}

func (p *PingPong) loop(stop, done chan struct{}) {
	defer close(done)
	const lane = 18
	pos, dir := 0, 1
	ticker := time.NewTicker(70 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		left, right, label, start := p.left, p.right, p.label, p.start
		p.mu.Unlock()
		cells := []rune(strings.Repeat("─", lane))
		cells[pos] = '●'
		trail := string(cells)
		line := fmt.Sprintf(" %s %s %s %s %s  %s %s",
			p.t.Bold(left), p.t.Moss("))"), p.t.Green(trail), p.t.Moss("(("), p.t.Bold(right),
			p.t.Lime(label), p.t.Gray("· "+Duration(time.Since(start).Round(time.Second))))
		fmt.Fprint(p.t.W, "\r\x1b[2K", line)
		pos += dir
		if pos == lane-1 || pos == 0 {
			dir = -dir
		}
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}
