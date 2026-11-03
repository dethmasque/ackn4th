package dash

import (
	"strings"
)

// canvas is a grid of truecolor pixels drawn with half-block characters:
// each terminal cell shows two pixels stacked, the upper as the
// foreground of "▀" and the lower as its background.
type canvas struct {
	w, h int // in pixels; h is twice the rows
	px   []rgb
	set  []bool
}

func newCanvas(w, rows int) *canvas {
	n := w * rows * 2
	return &canvas{w: w, h: rows * 2, px: make([]rgb, n), set: make([]bool, n)}
}

func (c *canvas) dot(x, y int, col rgb) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	c.px[y*c.w+x], c.set[y*c.w+x] = col, true
}

// glow blends col over whatever is at (x, y) by t.
func (c *canvas) glow(x, y int, col rgb, t float64, base rgb) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	under := base
	if c.set[y*c.w+x] {
		under = c.px[y*c.w+x]
	}
	c.dot(x, y, mix(under, col, t))
}

// rows renders the canvas; unset pixels show base.
func (c *canvas) rows(base rgb) []string {
	out := make([]string, c.h/2)
	for r := range out {
		var b strings.Builder
		for x := 0; x < c.w; x++ {
			ti, bi := (2*r)*c.w+x, (2*r+1)*c.w+x
			top, bot := base, base
			if c.set[ti] {
				top = c.px[ti]
			}
			if c.set[bi] {
				bot = c.px[bi]
			}
			if top == bot && !c.set[ti] {
				b.WriteString(base.bg() + " ")
				continue
			}
			b.WriteString(top.fg() + bot.bg() + "▀")
		}
		b.WriteString("\x1b[39m\x1b[49m")
		out[r] = b.String()
	}
	return out
}

// font is a 4×5 pixel alphabet for the wordmark.
var font = map[rune][5]string{
	'A': {".##.", "#..#", "####", "#..#", "#..#"},
	'C': {".###", "#...", "#...", "#...", ".###"},
	'K': {"#..#", "#.#.", "##..", "#.#.", "#..#"},
	'N': {"#..#", "##.#", "#.##", "#..#", "#..#"},
	'4': {"#..#", "#..#", "####", "...#", "...#"},
	'T': {"####", ".##.", ".##.", ".##.", ".##."},
	'H': {"#..#", "#..#", "####", "#..#", "#..#"},
}

const wordmark = "ACKN4TH"

// wordmarkWidth is the wordmark's width in pixels.
func wordmarkWidth() int { return len(wordmark)*5 - 1 }

// text draws s at (x, y) with a drop shadow and a top-to-bottom gradient.
func (c *canvas) text(s string, x, y int, top, bottom, shadow rgb) {
	for pass := 0; pass < 2; pass++ {
		cx := x
		for _, r := range s {
			g, ok := font[r]
			if !ok {
				cx += 5
				continue
			}
			for gy, line := range g {
				for gx, p := range line {
					if p != '#' {
						continue
					}
					if pass == 0 {
						c.dot(cx+gx+1, y+gy+1, shadow)
					} else {
						c.dot(cx+gx, y+gy, mix(top, bottom, float64(gy)/4))
					}
				}
			}
			cx += 5
		}
	}
}

// hash is a small deterministic scrambler for per-column randomness.
func hash(n int) int {
	n = (n ^ 61) ^ (n >> 16)
	n *= 9
	n ^= n >> 4
	n *= 0x27d4eb2d
	n ^= n >> 15
	if n < 0 {
		n = -n
	}
	return n
}

// rain draws falling streams of phosphor, dimmer where t is low.
func (c *canvas) rain(frame int, base rgb, strength func(x, y int) float64) {
	for x := 0; x < c.w; x++ {
		h := hash(x + 1)
		if h%5 > 1 {
			continue // sparse columns read as rain, not a wall
		}
		speed := 1 + h%3
		length := 2 + h%5
		period := c.h + length + 6 + h%9
		head := (frame*speed/2+h%97)%period - length/2
		for k := 0; k <= length; k++ {
			y := head - k
			if y < 0 || y >= c.h {
				continue
			}
			t := (1 - float64(k)/float64(length+1)) * (1 - float64(k)/float64(length+1)) * strength(x, y)
			col := mix(base, moss, t)
			if k == 0 {
				col = mix(base, lime, 0.9*strength(x, y))
			}
			c.glow(x, y, col, 1, base)
		}
	}
}

// pong draws two paddles volleying a ball: two agents, back and forth.
func (c *canvas) pong(x0, w, frame int, left, right rgb) {
	if w < 12 || c.h < 6 {
		return
	}
	span := w - 4
	pos := func(f int) (int, int) {
		p := f % (2 * span)
		bx := p
		if p >= span {
			bx = 2*span - p
		}
		vh := c.h - 2
		q := (f * 2 / 3) % (2 * vh)
		by := q
		if q >= vh {
			by = 2*vh - q
		}
		return x0 + 2 + bx, 1 + by
	}
	bx, by := pos(frame)
	for y := 0; y < c.h; y += 2 { // the net
		c.dot(x0+w/2, y, faint)
	}
	paddle := func(x, cy int, col rgb) {
		for dy := -1; dy <= 1; dy++ {
			c.dot(x, min(max(cy, 1), c.h-2)+dy, col)
		}
	}
	paddle(x0, by, left)
	paddle(x0+w-1, by, right)
	for t := 4; t >= 1; t-- { // the trail
		tx, ty := pos(frame - t)
		c.glow(tx, ty, mix(left, right, float64(tx-x0)/float64(w)), 0.5/float64(t), bg0)
	}
	c.dot(bx, by, white)
}
