package ui

import (
	"github.com/atterpac/dado/core"
	"github.com/atterpac/dado/theme"
	"github.com/gdamore/tcell/v2"
)

// Spark is a block-bar area chart: one column per sample, filled from the
// bottom. Solid block rendering carries far more ink than a braille line, so
// magnitude reads at a glance. Bars color by threshold band unless a custom
// color function is set.
type Spark struct {
	*core.Box
	values  []float64
	maxVal  float64 // 0 = auto-scale to the max of visible values
	label   string
	current string
	unit    string
	// thresholds in the same units as maxVal; default = % thresholds (70/90 of max).
	warn    float64
	crit    float64
	colorFn func(v, max float64) tcell.Color
}

// NewSpark constructs a Spark with default % thresholds (70/90 of max).
func NewSpark() *Spark {
	return &Spark{Box: new(core.Box)}
}

// SetLabel sets a one-line label rendered above the bars.
func (s *Spark) SetLabel(l string) *Spark { s.label = l; return s }

// SetMaxValue sets the value mapped to a full-height bar. Zero means auto-scale.
func (s *Spark) SetMaxValue(m float64) *Spark { s.maxVal = m; return s }

// SetCurrent sets the current value display (e.g. "42%").
func (s *Spark) SetCurrent(val string, unit string) *Spark {
	s.current = val
	s.unit = unit
	return s
}

// SetThresholds sets the absolute (warn, crit) value cutoffs for coloring.
// Zero means "use 70% / 90% of the effective max."
func (s *Spark) SetThresholds(warn, crit float64) *Spark {
	s.warn, s.crit = warn, crit
	return s
}

// SetColorFunc overrides threshold coloring with a custom bar color, resolved
// per draw so theme switches recolor live.
func (s *Spark) SetColorFunc(fn func(v, max float64) tcell.Color) *Spark {
	s.colorFn = fn
	return s
}

// SetValues replaces all values.
func (s *Spark) SetValues(v []float64) *Spark { s.values = v; return s }

// AddValue appends a value and trims to keep at most max points.
func (s *Spark) AddValue(v float64, max int) *Spark {
	s.values = append(s.values, v)
	if len(s.values) > max {
		s.values = s.values[len(s.values)-max:]
	}
	return s
}

// Draw renders the Spark inside its inner rect.
func (s *Spark) Draw(screen tcell.Screen) {
	s.Box.DrawForSubclass(screen)
	x, y, w, h := s.GetInnerRect()
	if w <= 0 || h <= 0 {
		return
	}

	plotTop := y
	plotH := h
	linesUsed := 0

	// Draw label if present
	if s.label != "" && h > 1 {
		drawLabel(screen, x, y, w, s.label)
		plotTop = y + 1
		plotH = h - 1
		linesUsed++
	}

	// Draw current value if present (above the sparkline)
	if s.current != "" && h > 2 && linesUsed < h {
		drawCurrentValue(screen, x, plotTop, w, s.current, s.unit)
		plotTop++
		plotH--
		linesUsed++
	}

	if plotH <= 0 || len(s.values) == 0 {
		return
	}

	maxVal := s.maxVal
	if maxVal == 0 {
		for _, v := range s.values {
			if v > maxVal {
				maxVal = v
			}
		}
		if maxVal == 0 {
			maxVal = 1
		}
	}

	warn, crit := s.warn, s.crit
	if warn == 0 {
		warn = maxVal * 0.70
	}
	if crit == 0 {
		crit = maxVal * 0.90
	}

	levels := []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

	// Every cell must carry the theme background explicitly: tcell.StyleDefault
	// means the *terminal's* default background, which shows through partial
	// blocks and baselines as light/dark stripes the moment the theme differs
	// from the terminal.
	bg := theme.Bg()
	baseStyle := tcell.StyleDefault.Foreground(theme.FgDim()).Background(bg).Dim(true)

	start := 0
	if len(s.values) > w {
		start = len(s.values) - w
	}
	// Right-align when there are fewer samples than columns: the newest sample
	// always sits at the right edge, like every other timeline on screen.
	xoff := x + w - (len(s.values) - start)

	for i := start; i < len(s.values); i++ {
		col := xoff + (i - start)
		v := s.values[i]
		if v < 0 {
			v = 0
		}
		if v > maxVal {
			v = maxVal
		}
		units := int(v / maxVal * float64(plotH) * 8)
		if v > 0 && units == 0 {
			units = 1 // any activity gets at least a sliver
		}
		fullCells := units / 8
		partial := units % 8

		if units == 0 {
			// Idle baseline: keeps the timeline visible instead of leaving
			// quiet stretches as empty space.
			screen.SetContent(col, plotTop+plotH-1, '▁', nil, baseStyle)
			continue
		}

		var color tcell.Color
		if s.colorFn != nil {
			color = s.colorFn(v, maxVal)
		} else {
			color = thresholdColor(v, warn, crit)
		}
		style := tcell.StyleDefault.Foreground(color).Background(bg)

		for r := 0; r < fullCells && r < plotH; r++ {
			screen.SetContent(col, plotTop+plotH-1-r, '█', nil, style)
		}
		if fullCells < plotH && partial > 0 {
			screen.SetContent(col, plotTop+plotH-1-fullCells, levels[partial], nil, style)
		}
	}
}

// CoreGrid renders per-core CPU history as a core × time heatmap. Each text
// row packs two cores using the upper-half block: the foreground colors the
// top core, the background the bottom one, so 20 cores fit in 10 rows. Color
// blends from the theme background (idle) through success → warning → error
// as a core saturates, which keeps the map readable on light and dark themes.
type CoreGrid struct {
	*core.Box
	cores [][]float64 // rolling window per core, newest sample last
}

// NewCoreGrid constructs an empty CoreGrid.
func NewCoreGrid() *CoreGrid {
	return &CoreGrid{Box: new(core.Box)}
}

// Update appends a fresh per-core sample. New cores start zero-filled so the
// map renders full-width immediately.
func (g *CoreGrid) Update(values []float64, hist int) {
	if hist < 1 {
		hist = 1
	}
	for len(g.cores) < len(values) {
		g.cores = append(g.cores, make([]float64, hist))
	}
	for i, v := range values {
		r := g.cores[i]
		copy(r, r[1:])
		r[len(r)-1] = v
	}
}

// Reset zeroes all per-core histories in place, keeping the map visible while
// a node is down.
func (g *CoreGrid) Reset() {
	for _, r := range g.cores {
		for i := range r {
			r[i] = 0
		}
	}
}

// Draw renders the heatmap: columns are time (newest at the right edge, one
// sample per column), rows are core pairs.
func (g *CoreGrid) Draw(screen tcell.Screen) {
	g.Box.DrawForSubclass(screen)
	x, y, w, h := g.GetInnerRect()
	n := len(g.cores)
	if n == 0 || w <= 0 || h <= 0 {
		return
	}
	rows := (n + 1) / 2
	if rows > h {
		rows = h
	}
	bg := theme.Bg()
	for row := 0; row < rows; row++ {
		for col := 0; col < w; col++ {
			fg := g.cellColor(2*row, col, w, bg)
			bgc := g.cellColor(2*row+1, col, w, bg)
			screen.SetContent(x+col, y+row, '▀', nil,
				tcell.StyleDefault.Foreground(fg).Background(bgc))
		}
	}
}

// cellColor maps core c's sample at screen column col to a heat color.
// Columns cover the last w samples; columns older than the history are idle.
func (g *CoreGrid) cellColor(c, col, w int, bg tcell.Color) tcell.Color {
	if c >= len(g.cores) {
		return bg
	}
	r := g.cores[c]
	idx := len(r) - w + col
	if idx < 0 || idx >= len(r) {
		return bg
	}
	return heatColor(r[idx]/100, bg)
}

// heatColor maps a 0..1 utilization to a color: idle stays at the theme
// background, light activity tints toward success, heavy load walks through
// warning into error. A floor on the first band keeps small loads visible.
func heatColor(f float64, bg tcell.Color) tcell.Color {
	switch {
	case f <= 0.02:
		return bg
	case f < 0.5:
		return blend(bg, theme.Success(), 0.30+0.70*(f/0.5))
	case f < 0.8:
		return blend(theme.Success(), theme.Warning(), (f-0.5)/0.3)
	default:
		t := (f - 0.8) / 0.2
		if t > 1 {
			t = 1
		}
		return blend(theme.Warning(), theme.Error(), t)
	}
}

// blend linearly interpolates between two colors. Colors that don't resolve
// to RGB (e.g. terminal default) fall through to the target color.
func blend(a, b tcell.Color, t float64) tcell.Color {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	ar, ag, ab := a.RGB()
	br, bgc, bb := b.RGB()
	if ar < 0 || br < 0 {
		return b
	}
	lerp := func(x, y int32) int32 { return x + int32(float64(y-x)*t) }
	return tcell.NewRGBColor(lerp(ar, br), lerp(ag, bgc), lerp(ab, bb))
}

// ---- shared helpers -----------------------------------------------------------

func drawLabel(screen tcell.Screen, x, y, w int, label string) {
	style := tcell.StyleDefault.Foreground(theme.FgDim()).Background(theme.Bg())
	col := x
	for _, r := range label {
		if col >= x+w {
			return
		}
		screen.SetContent(col, y, r, nil, style)
		col++
	}
}

func drawCurrentValue(screen tcell.Screen, x, y, w int, value, unit string) {
	style := tcell.StyleDefault.Foreground(theme.Fg()).Background(theme.Bg()).Bold(true)
	col := x
	text := value + " " + unit
	for _, r := range text {
		if col >= x+w {
			return
		}
		screen.SetContent(col, y, r, nil, style)
		col++
	}
}

func thresholdColor(v, warn, crit float64) tcell.Color {
	switch {
	case v >= crit:
		return tcell.ColorRed
	case v >= warn:
		return tcell.ColorYellow
	default:
		return tcell.ColorGreen
	}
}
