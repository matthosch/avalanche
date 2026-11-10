package main

import (
	"math"
	"strconv"
)

// A flow is a diagram of services that a packet travels between, one hop at a time, while a
// caption explains each beat.

type flowNode struct {
	id, title, sub string
	x, y, w, h     float64
	accent         rgb
	lines          []string // what the node shows at the start
}

type flowStep struct {
	from, to string
	label    string
	col      rgb
	crash    bool                // from crashes; to is unused
	set      map[string][]string // node lines once the step finishes
}

type flowBeat struct {
	caption string
	steps   []flowStep
}

type lane struct {
	label string
	y     float64
}

type flowScene struct {
	title     string
	nodes     []*flowNode
	edges     [][2]string
	lanes     []lane
	separator float64 // y of the dashed line between lanes, or 0
	beats     []flowBeat
	outro     string
	stepDur   float64
}

type timedStep struct {
	flowStep
	beat       int
	start, end float64
}

const (
	flowLead   = 0.6 // pause before the first beat
	beatPause  = 0.7
	crashDur   = 1.2
	outroDur   = 0.1 // the outro caption is held by the encoder
	captionTop = 432.0
)

func (s *flowScene) schedule() ([]timedStep, []float64, float64) {
	var steps []timedStep
	var beatStarts []float64
	t := flowLead
	for i, b := range s.beats {
		beatStarts = append(beatStarts, t)
		for _, st := range b.steps {
			d := s.stepDur
			if st.crash {
				d = crashDur
			}
			steps = append(steps, timedStep{flowStep: st, beat: i, start: t, end: t + d})
			t += d
		}
		t += beatPause
	}
	return steps, beatStarts, t
}

func (s *flowScene) duration() float64 {
	_, _, end := s.schedule()
	return end + outroDur
}

func (s *flowScene) node(id string) *flowNode {
	for _, n := range s.nodes {
		if n.id == id {
			return n
		}
	}
	panic("no node " + id)
}

// exit is where the line from n's centre toward (tx, ty) leaves n's box.
func exit(n *flowNode, tx, ty float64) (float64, float64) {
	cx, cy := n.x+n.w/2, n.y+n.h/2
	dx, dy := tx-cx, ty-cy
	t := math.Inf(1)
	if dx != 0 {
		t = math.Min(t, (n.w/2)/math.Abs(dx))
	}
	if dy != 0 {
		t = math.Min(t, (n.h/2)/math.Abs(dy))
	}
	return cx + dx*t, cy + dy*t
}

func ends(a, b *flowNode) (x0, y0, x1, y1 float64) {
	x0, y0 = exit(a, b.x+b.w/2, b.y+b.h/2)
	x1, y1 = exit(b, a.x+a.w/2, a.y+a.h/2)
	return
}

func (s *flowScene) draw(c *canvas, t float64) {
	steps, beatStarts, end := s.schedule()

	// State: every finished step's changes, and the step under way.
	lines := map[string][]string{}
	for _, n := range s.nodes {
		lines[n.id] = n.lines
	}
	var active *timedStep
	for i := range steps {
		st := &steps[i]
		if t >= st.end {
			for id, l := range st.set {
				lines[id] = l
			}
		} else if t >= st.start {
			active = st
		}
	}
	beat := -1
	for i, b := range beatStarts {
		if t >= b {
			beat = i
		}
	}

	c.clear(colBG)
	c.text(s.title, 30, 38, face(bold, 22), colText, 1, left)
	for _, l := range s.lanes {
		c.text(l.label, 30, l.y, face(monoBold, 11), colMuted, 1, left)
	}
	if s.separator > 0 {
		c.dashed(30, s.separator, float64(c.w)-30, s.separator, 1, 6, 6, colBorder, 1)
	}

	for _, e := range s.edges {
		x0, y0, x1, y1 := ends(s.node(e[0]), s.node(e[1]))
		c.line(x0, y0, x1, y1, 1.5, colBorder, 1)
	}
	if active != nil && !active.crash {
		x0, y0, x1, y1 := ends(s.node(active.from), s.node(active.to))
		c.line(x0, y0, x1, y1, 2.5, active.col, 0.9)
	}

	for _, n := range s.nodes {
		hot, crashed := false, 0.0
		if active != nil {
			hot = active.from == n.id || (!active.crash && active.to == n.id)
			if active.crash && active.from == n.id {
				crashed = 0.55 + 0.45*math.Sin(phase(t, active.start, active.end-active.start)*math.Pi*4)
			}
		}
		s.drawNode(c, n, lines[n.id], hot, crashed)
	}

	if active != nil && !active.crash {
		x0, y0, x1, y1 := ends(s.node(active.from), s.node(active.to))
		p := ease(phase(t, active.start, (active.end-active.start)*0.85))
		px, py := x0+(x1-x0)*p, y0+(y1-y0)*p
		c.circle(px, py, 13, active.col, 0.25)
		c.circle(px, py, 7, active.col, 1)
		ox, oy := s.labelOffset(c, active)
		c.pill(active.label, px+ox, py+oy, face(monoBold, 12), active.col, colBG, 1)
	}

	caption, number := s.outro, ""
	if beat >= 0 && t < end {
		caption, number = s.beats[beat].caption, strconv.Itoa(beat+1)
	}
	if beat >= 0 {
		drawCaption(c, number, caption)
	}
}

func (s *flowScene) drawNode(c *canvas, n *flowNode, lines []string, hot bool, crashed float64) {
	fill, border, bw := colPanel, colBorder, 1.5
	if hot {
		fill, border, bw = mix(colPanel, n.accent, 0.12), n.accent, 2.5
	}
	if crashed > 0 {
		fill, border, bw = mix(colPanel, colRed, 0.25*crashed), colRed, 2.5
	}
	c.box(n.x, n.y, n.w, n.h, 10, fill, border, bw)
	c.roundRect(n.x+bw, n.y+12, 4, n.h-24, 2, n.accent, 1)
	c.text(n.title, n.x+16, n.y+24, face(bold, 15), colText, 1, left)
	c.text(n.sub, n.x+16, n.y+41, face(regular, 12), colMuted, 1, left)
	for i, l := range lines {
		c.text(l, n.x+16, n.y+60+float64(i)*16, face(mono, 12), colText, 1, left)
	}
	if crashed > 0 {
		c.pill("× crashed", n.x+n.w/2, n.y-2, face(monoBold, 12), colRed, colBG, 1)
	}
}

// drawCaption is the explanation bar along the bottom, with the beat's number.
func drawCaption(c *canvas, number, caption string) {
	x, w, h := 30.0, float64(c.w)-60, 88.0
	c.box(x, captionTop, w, h, 12, colPanel, colBorder, 1.5)
	tx := x + 22
	if number != "" {
		c.circle(x+36, captionTop+h/2, 16, colBlue, 1)
		c.text(number, x+36, captionTop+h/2+6, face(bold, 16), colBG, 1, center)
		tx = x + 66
	}
	f := face(regular, 17)
	ls := wrap(f, caption, x+w-22-tx)
	y := captionTop + h/2 + 6 - float64(len(ls)-1)*12
	for i, l := range ls {
		c.text(l, tx, y+float64(i)*24, f, colText, 1, left)
	}
}

// labelOffset is where a step's label sits relative to its packet: of a few spots around the
// path, the one that covers the least of any box over the whole hop, so it doesn't jump about.
func (s *flowScene) labelOffset(c *canvas, st *timedStep) (float64, float64) {
	w := textWidth(face(monoBold, 12), st.label) + 16
	candidates := [][2]float64{{0, -24}, {0, 24}, {0, -58}, {0, 58}, {w/2 + 16, 0}, {-w/2 - 16, 0}}
	x0, y0, x1, y1 := ends(s.node(st.from), s.node(st.to))
	overlap := func(ax, ay, aw, ah, bx, by, bw, bh float64) float64 {
		ox := math.Max(0, math.Min(ax+aw, bx+bw)-math.Max(ax, bx))
		oy := math.Max(0, math.Min(ay+ah, by+bh)-math.Max(ay, by))
		return ox * oy
	}
	best, bestCost := candidates[0], math.Inf(1)
	for i, o := range candidates {
		cost := float64(i) // prefer earlier spots when otherwise equal
		for _, p := range []float64{0.1, 0.3, 0.5, 0.7, 0.9} {
			cx, cy := x0+(x1-x0)*p+o[0], y0+(y1-y0)*p+o[1]
			lx, ly := cx-w/2, cy-11
			for _, n := range s.nodes {
				cost += overlap(lx, ly, w, 22, n.x, n.y, n.w, n.h)
			}
			cost += overlap(lx, ly, w, 22, 0, captionTop, float64(c.w), float64(c.h)) * 2
			if lx < 8 || lx+w > float64(c.w)-8 || ly < 50 {
				cost += 5000
			}
		}
		if cost < bestCost {
			best, bestCost = o, cost
		}
	}
	return best[0], best[1]
}
