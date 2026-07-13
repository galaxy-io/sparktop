// Package ui renders the sparktop dashboard with dado (tcell under the hood).
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/atterpac/dado/components"
	"github.com/atterpac/dado/core"
	"github.com/atterpac/dado/layout"
	"github.com/atterpac/dado/theme"
	"github.com/atterpac/dado/theme/themes"
	"github.com/gdamore/tcell/v2"

	"github.com/galaxy-io/sparktop/internal/config"
	"github.com/galaxy-io/sparktop/internal/metrics"
)

// DGX Spark (GB10) operating envelope. The GB10 superchip shares a single
// ~128 GB LPDDR5X pool between the Grace CPU and Blackwell GPU over NVLink-C2C,
// so "system memory" and "VRAM" are the same physical pool — we show one
// unified figure rather than two competing graphs.
const (
	tempWarnC  = 80.0  // GPU junction warming
	tempCritC  = 87.0  // throttle territory on a compact chassis
	powerWarnW = 110.0 // approaching the ~140 W envelope
	powerCritW = 135.0
	powerMaxW  = 150.0 // sparkline full-scale
	memWarnPct = 80.0
	memCritPct = 92.0
	// throttleClockFrac: a GPU pinned busy whose SM clock has fallen well below
	// the peak we've observed is almost certainly therm/power throttling.
	throttleClockFrac = 0.85
	throttleUtilPct   = 80.0
)

// ring is a fixed-width rolling window; the newest sample is always last.
// Feeding graphs a full-width window (zero-padded at first) makes them render
// edge-to-edge immediately instead of growing in from one side.
type ring struct{ v []float64 }

func newRing(n int) *ring {
	if n < 1 {
		n = 1
	}
	return &ring{v: make([]float64, n)}
}

func (r *ring) push(x float64) []float64 {
	copy(r.v, r.v[1:])
	r.v[len(r.v)-1] = x
	return r.v
}

// nodeWidgets are the live widgets for one node column.
type nodeWidgets struct {
	panel      *components.Panel
	body       *core.Flex
	status     *core.TextView
	health     *components.ProgressBar
	cards      *core.Flex // 2×2 KPI grid
	cardUtil   *components.MetricCard
	cardTemp   *components.MetricCard
	cardPower  *components.MetricCard
	cardMem    *components.MetricCard
	gpuLine    *core.TextView // per-GPU detail (clock is the throttle tell)
	inferLine  *core.TextView // vLLM serving stats (hidden when no vllm_port)
	inferStats *core.TextView // vLLM latency detail: TTFT, ITL, prefill, preemptions
	tok        *Spark         // generation throughput as a block-bar area
	cores      *CoreGrid      // per-core CPU heatmap (core × time)
	coresCap   *core.TextView // host stats caption above the map
	coresBox   *core.Flex     // cores with its caption
	netCap     *core.TextView // live rx/tx figures
	netRx      *Spark
	netTx      *Spark
	netBox     *core.Flex
	diskCap    *core.TextView // live rd/wr figures
	diskRd     *Spark
	diskWr     *Spark
	diskBox    *core.Flex

	// dynamic body heights; dado's Flex has no ResizeItem, so the body is
	// re-assembled whenever one of these changes.
	gpuRows    int
	inferRows  int
	infer2Rows int
	tokProp    int

	// full-width rolling histories behind the sparklines and graphs
	histUtil  *ring
	histTemp  *ring
	histPower *ring
	histMem   *ring
	histTok   *ring
	histRx    *ring
	histTx    *ring
	histRd    *ring
	histWr    *ring

	// rolling state for trend arrows and throttle detection
	maxClock  map[string]float64
	prevUtil  float64
	prevTemp  float64
	prevPower float64
	prevMem   float64
	havePrev  bool
}

// UI owns the dado application and the per-node widgets.
type UI struct {
	app       *layout.App
	cfg       *config.Config
	top       *core.TextView
	nodes     []*nodeWidgets
	hist      int
	themeName string
	onForce   func()
}

// New builds the dashboard for the given nodes (it does not start it).
func New(cfg *config.Config, nodes []config.Node) *UI {
	u := &UI{cfg: cfg, hist: cfg.HistoryLen(), themeName: cfg.Theme}

	u.top = core.NewTextView().SetDynamicColors(true)

	cluster := core.NewFlex().SetDirection(core.Row)
	for _, n := range nodes {
		w := newNodeWidgets(n.Display(), u.hist)
		u.nodes = append(u.nodes, w)
		cluster.AddItem(w.panel, 0, 1, false)
	}

	root := components.NewComponentBase(cluster).
		SetName("cluster").
		SetHints([]components.KeyHint{
			{Key: "r", Description: "refresh now"},
			{Key: "t", Description: "theme"},
			{Key: "q", Description: "quit"},
		})

	u.app = layout.NewApp(layout.AppConfig{
		TopBar:       u.top,
		TopBarHeight: 1,
	})
	u.app.Pages().Push(root)
	u.app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyCtrlC {
			u.app.Stop()
			return nil
		}
		switch ev.Rune() {
		case 'q', 'Q':
			u.app.Stop()
			return nil
		case 'r', 'R':
			if u.onForce != nil {
				u.onForce()
			}
			return nil
		case 't', 'T':
			u.cycleTheme()
			return nil
		}
		return ev
	})
	return u
}

func newNodeWidgets(title string, hist int) *nodeWidgets {
	// Charts draw one column per sample with the newest at the right edge, so
	// keep more history than any realistic panel width and let each chart show
	// the newest samples that fit.
	if hist < 256 {
		hist = 256
	}
	w := &nodeWidgets{
		status:     core.NewTextView().SetDynamicColors(true),
		health:     components.NewProgressBar().SetShowPercentage(true),
		cardUtil:   kpiCard("GPU", "%", 100),
		cardTemp:   kpiCard("Temp", "°C", 100).SetThresholds(tempWarnC, tempCritC, true),
		cardPower:  kpiCard("Power", "W", powerMaxW).SetThresholds(powerWarnW, powerCritW, true),
		cardMem:    kpiCard("Mem", "%", 100).SetThresholds(memWarnPct, memCritPct, true),
		gpuLine:    core.NewTextView().SetDynamicColors(true),
		inferLine:  core.NewTextView().SetDynamicColors(true),
		inferStats: core.NewTextView().SetDynamicColors(true),
		tok:        NewSpark().SetLabel("tok/s").SetColorFunc(flatColor(theme.Success)),
		cores:      NewCoreGrid(),
		coresCap:   core.NewTextView().SetDynamicColors(true),
		netCap:     core.NewTextView().SetDynamicColors(true),
		netRx:      NewSpark().SetColorFunc(flatColor(theme.Accent)),
		netTx:      NewSpark().SetColorFunc(flatColor(theme.Success)),
		diskCap:    core.NewTextView().SetDynamicColors(true),
		diskRd:     NewSpark().SetColorFunc(flatColor(theme.Accent)),
		diskWr:     NewSpark().SetColorFunc(flatColor(theme.Success)),
		gpuRows:    1,
		inferRows:  1,
		infer2Rows: 1,
		tokProp:    3,
		histUtil:   newRing(hist),
		histTemp:   newRing(hist),
		histPower:  newRing(hist),
		histMem:    newRing(hist),
		histTok:    newRing(hist),
		histRx:     newRing(hist),
		histTx:     newRing(hist),
		histRd:     newRing(hist),
		histWr:     newRing(hist),
		maxClock:   map[string]float64{},
	}

	// 2×2 KPI grid: the four signals that actually predict a Spark falling over.
	cardsTop := core.NewFlex().SetDirection(core.Row).
		AddItem(w.cardUtil, 0, 1, false).
		AddItem(w.cardTemp, 0, 1, false)
	cardsBot := core.NewFlex().SetDirection(core.Row).
		AddItem(w.cardPower, 0, 1, false).
		AddItem(w.cardMem, 0, 1, false)
	w.cards = core.NewFlex().SetDirection(core.Column).
		AddItem(cardsTop, 0, 1, false).
		AddItem(cardsBot, 0, 1, false)

	w.coresCap.SetText("[gray]cores")
	w.coresBox = core.NewFlex().SetDirection(core.Column).
		AddItem(w.coresCap, 1, 0, false).
		AddItem(w.cores, 0, 1, false)
	w.netBox = core.NewFlex().SetDirection(core.Column).
		AddItem(w.netCap, 1, 0, false).
		AddItem(w.netRx, 0, 1, false).
		AddItem(w.netTx, 0, 1, false)
	w.diskBox = core.NewFlex().SetDirection(core.Column).
		AddItem(w.diskCap, 1, 0, false).
		AddItem(w.diskRd, 0, 1, false).
		AddItem(w.diskWr, 0, 1, false)
	w.body = core.NewFlex().SetDirection(core.Column)
	w.layoutBody()

	w.panel = components.NewPanel().SetTitle(" " + title + " ")
	w.panel.SetContent(w.body)
	return w
}

// layoutBody (re)assembles the body column. A dado card draws 4 content rows
// (label, value, trend, sparkline) plus its border, so 6 rows per card row
// keeps the 2×2 grid tight with no empty band inside the cards.
func (w *nodeWidgets) layoutBody() {
	w.body.Clear()
	w.body.AddItem(w.status, 1, 0, false)
	w.body.AddItem(w.health, 1, 0, false)
	w.body.AddItem(w.cards, 12, 0, false)
	w.body.AddItem(w.gpuLine, w.gpuRows, 0, false)
	w.body.AddItem(w.inferLine, w.inferRows, 0, false)
	w.body.AddItem(w.inferStats, w.infer2Rows, 0, false)
	w.body.AddItem(w.tok, 0, w.tokProp, false)
	w.body.AddItem(w.coresBox, 0, 2, false)
	w.body.AddItem(w.netBox, 0, 2, false)
	w.body.AddItem(w.diskBox, 0, 2, false)
}

// setRows adjusts the dynamic body heights, rebuilding the layout on change.
func (w *nodeWidgets) setRows(gpuRows, inferRows, infer2Rows, tokProp int) {
	if gpuRows == w.gpuRows && inferRows == w.inferRows &&
		infer2Rows == w.infer2Rows && tokProp == w.tokProp {
		return
	}
	w.gpuRows, w.inferRows, w.infer2Rows, w.tokProp = gpuRows, inferRows, infer2Rows, tokProp
	w.layoutBody()
}

// kpiCard builds a metric card with a fixed-scale sparkline.
func kpiCard(label, unit string, sparkMax float64) *components.MetricCard {
	return components.NewMetricCard().
		SetLabel(label).
		SetUnit(unit).
		SetCompact(false).
		SetShowBorder(true).
		SetSparklineMax(sparkMax)
}

// SetOnForceRefresh registers a callback invoked when the user presses "r".
func (u *UI) SetOnForceRefresh(fn func()) { u.onForce = fn }

// Run starts the event loop and blocks until the app stops.
func (u *UI) Run() error { return u.app.Run() }

// Stop tears down the app.
func (u *UI) Stop() { u.app.Stop() }

// Submit applies a fresh batch of snapshots; it is safe to call from any goroutine.
func (u *UI) Submit(snaps []metrics.Snapshot) {
	u.app.QueueUpdateDraw(func() { u.render(snaps) })
}

// cycleTheme advances to the next built-in dado theme. SetProvider queues the
// redraw off this goroutine, so it is safe to call from the input capture.
func (u *UI) cycleTheme() {
	names := themes.Names()
	if len(names) == 0 {
		return
	}
	next := names[0]
	for i, n := range names {
		if n == u.themeName {
			next = names[(i+1)%len(names)]
			break
		}
	}
	u.themeName = next
	theme.SetProvider(themes.Get(next))
}

func (u *UI) render(snaps []metrics.Snapshot) {
	upNodes, upGPUs := 0, 0
	var clusterPower, maxTemp float64
	for _, s := range snaps {
		if s.Up {
			upNodes++
		}
		upGPUs += s.UpGPUs()
		for _, g := range s.GPUs {
			clusterPower += g.PowerW
			if g.TempC > maxTemp {
				maxTemp = g.TempC
			}
		}
	}
	u.top.SetText(fmt.Sprintf(
		" [::b]sparktop[::-]  •  %d/%d nodes  •  %d GPU(s)  •  %.0f W  •  %s peak  •  %s  •  every %s",
		upNodes, len(snaps), upGPUs, clusterPower, tempTag(maxTemp),
		time.Now().Format("15:04:05"), u.cfg.PollEvery()))

	for i := range u.nodes {
		if i >= len(snaps) {
			continue
		}
		u.renderNode(u.nodes[i], snaps[i])
	}
}

func (u *UI) renderNode(w *nodeWidgets, s metrics.Snapshot) {
	if !s.Up {
		w.markDown(s)
		return
	}

	// Aggregate the GPU(s) into the headline figures the cards show.
	var maxUtil, maxTemp, sumPower float64
	throttling := false
	for _, g := range s.GPUs {
		if g.UtilPct > maxUtil {
			maxUtil = g.UtilPct
		}
		if g.TempC > maxTemp {
			maxTemp = g.TempC
		}
		sumPower += g.PowerW
		if g.SMClockMHz > w.maxClock[g.Index] {
			w.maxClock[g.Index] = g.SMClockMHz
		}
		if g.UtilPct >= throttleUtilPct && g.SMClockMHz > 0 &&
			g.SMClockMHz < throttleClockFrac*w.maxClock[g.Index] {
			throttling = true
		}
	}

	health := healthScore(s, maxTemp, throttling)

	// Status line: model · uptime · freshness, plus any partial-failure badges.
	model := "node"
	if len(s.GPUs) > 0 {
		model = shortModel(s.GPUs[0].Model)
	}
	status := fmt.Sprintf("[white]%s[-]  [gray]up %s · %s ago", model,
		humanUptime(s.Uptime), humanDur(time.Since(s.When)))
	if throttling {
		status += "  [black:red] THROTTLING [-:-]"
	}
	if !s.GPUUp {
		status += "  [yellow]gpu: " + firstLine(s.GPUErr)
	}
	w.status.SetText(status)

	w.health.SetProgress(clamp01(health / 100))
	w.panel.SetTitleColor(healthColor(health))

	// KPI cards. Util is "busy" not "bad", so its trend reads green; temp/power/
	// mem rising is bad, so theirs reads red.
	memPct := s.MemUsedPct
	setCard(w.cardUtil, w.histUtil, 100, maxUtil, w.prevUtil, w.havePrev, true)
	setCard(w.cardTemp, w.histTemp, 100, maxTemp, w.prevTemp, w.havePrev, false)
	setCard(w.cardPower, w.histPower, powerMaxW, sumPower, w.prevPower, w.havePrev, false)
	setCard(w.cardMem, w.histMem, 100, memPct, w.prevMem, w.havePrev, false)
	w.prevUtil, w.prevTemp, w.prevPower, w.prevMem = maxUtil, maxTemp, sumPower, memPct
	w.havePrev = true

	w.renderGPULine(s)
	w.renderInfer(s)

	// Host pressure caption: the inputs to the health score, made visible.
	w.coresCap.SetText(fmt.Sprintf(
		"[gray]cores · cpu %s%.0f%%[-] [gray]· load %s%.1f[-][gray]/%d · rootfs %s%.0f%%[-]",
		colorTag(s.CPUPct, 75, 90), s.CPUPct,
		colorTag(s.Load1, float64(s.NCPU), float64(s.NCPU)*2), s.Load1, s.NCPU,
		colorTag(s.RootFSPct, 80, 90), s.RootFSPct))
	if len(s.PerCoreUtil) > 0 {
		w.cores.Update(s.PerCoreUtil, u.hist)
	}
	w.feedNet(s.NetRxBps, s.NetTxBps)
	w.feedDisk(s.DiskRBps, s.DiskWBps)
}

// feedNet pushes fresh rx/tx samples and refreshes the caption's live figures.
func (w *nodeWidgets) feedNet(rx, tx float64) {
	w.netRx.SetValues(w.histRx.push(rx))
	w.netTx.SetValues(w.histTx.push(tx))
	w.netCap.SetText(fmt.Sprintf("[gray]net ·[-] %srx %s/s[-] [gray]·[-] %stx %s/s[-]",
		tag(theme.Accent()), humanBytes(rx), tag(theme.Success()), humanBytes(tx)))
}

// feedDisk pushes fresh read/write samples and refreshes the caption.
func (w *nodeWidgets) feedDisk(rd, wr float64) {
	w.diskRd.SetValues(w.histRd.push(rd))
	w.diskWr.SetValues(w.histWr.push(wr))
	w.diskCap.SetText(fmt.Sprintf("[gray]disk ·[-] %srd %s/s[-] [gray]·[-] %swr %s/s[-]",
		tag(theme.Accent()), humanBytes(rd), tag(theme.Success()), humanBytes(wr)))
}

// feedTok pushes a fresh generation-rate sample.
func (w *nodeWidgets) feedTok(v float64) {
	w.tok.SetValues(w.histTok.push(v))
}

// flatColor paints every bar of a rate chart in one theme color, resolved at
// draw time so theme switches recolor it.
func flatColor(c func() tcell.Color) func(float64, float64) tcell.Color {
	return func(float64, float64) tcell.Color { return c() }
}

// tag renders a tcell color as a dynamic-color open tag for TextView text.
func tag(c tcell.Color) string {
	r, g, b := c.RGB()
	if r < 0 {
		return "[white]"
	}
	return fmt.Sprintf("[#%02x%02x%02x]", r, g, b)
}

// renderGPULine shows one row per GPU. The SM clock is the value that exposes
// throttling, which none of the cards surface on their own.
func (w *nodeWidgets) renderGPULine(s metrics.Snapshot) {
	if len(s.GPUs) == 0 {
		w.gpuLine.SetText("[gray]no GPUs reported")
		w.setRows(1, w.inferRows, w.infer2Rows, w.tokProp)
		return
	}
	// No framebuffer figure: on GB10's unified memory dcgm reports 0 MiB of
	// dedicated VRAM, so the Mem card (system memory) is the real pool. Clock is
	// kept because it's the throttle tell.
	var b strings.Builder
	for i, g := range s.GPUs {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "gpu%s %s%4.0f%% util[-] · %s · [gray]%.0f MHz · %.0f W",
			g.Index, utilTag(g.UtilPct), g.UtilPct,
			tempTag(g.TempC), g.SMClockMHz, g.PowerW)
	}
	w.gpuLine.SetText(b.String())
	w.setRows(len(s.GPUs), w.inferRows, w.infer2Rows, w.tokProp)
}

// renderInfer shows vLLM serving health and toggles the workload widgets. When
// no vllm_port is configured the line and tok/s graph collapse to zero height.
func (w *nodeWidgets) renderInfer(s metrics.Snapshot) {
	if !s.InferOn {
		w.setRows(w.gpuRows, 0, 0, 0)
		return
	}

	if !s.InferUp {
		w.setRows(w.gpuRows, 1, 0, 3)
		w.inferLine.SetText("[yellow]vLLM: " + firstLine(s.InferErr))
		w.feedTok(0) // keep the timeline sliding so the outage reads as a dip
		return
	}
	w.setRows(w.gpuRows, 1, 1, 3)

	model := s.InferModel
	if model == "" {
		model = "vLLM"
	}
	waitTag := "[gray]"
	if s.ReqWaiting > 0 {
		waitTag = "[yellow]" // queue building = backpressure
	}
	w.inferLine.SetText(fmt.Sprintf(
		"[white]%s[-]  [green]%.0f run[-] · %s%.0f wait[-] · KV %s%.0f%%[-] · [::b]%s tok/s",
		shortModel(model), s.ReqRunning, waitTag, s.ReqWaiting,
		colorTag(s.KVCachePct, 80, 95), s.KVCachePct, humanCount(s.GenTokPerSec)))

	// Latency detail: TTFT is what a user feels before streaming starts, ITL is
	// the streaming smoothness, prefill throughput shows ingest load, and any
	// preemption means the KV cache is thrashing.
	pre := fmt.Sprintf("[gray]%.0f preempt[-]", s.Preemptions)
	if s.Preemptions > 0 {
		pre = fmt.Sprintf("[yellow]%.0f preempt[-]", s.Preemptions)
	}
	w.inferStats.SetText(fmt.Sprintf(
		"[gray]ttft[-] %s [gray]· itl[-] %s [gray]· prefill[-] %s [gray]tok/s ·[-] %s",
		latencyVal(s.TTFTMs, 1000, 3000),
		latencyVal(s.ITLMs, 25, 60),
		humanCount(s.PromptTokPerSec), pre))
	w.feedTok(s.GenTokPerSec)
}

// markDown blanks a node's widgets and shows why it's unreachable.
func (w *nodeWidgets) markDown(s metrics.Snapshot) {
	w.status.SetText("[red::b]DOWN[-:-]  [gray]" + firstLine(s.Err))
	w.health.SetProgress(0)
	w.panel.SetTitleColor(tcell.ColorRed)
	for _, c := range []*components.MetricCard{w.cardUtil, w.cardTemp, w.cardPower, w.cardMem} {
		c.SetValue("—").SetSparkline(nil).SetTrend(components.TrendNeutral, "", true)
	}
	w.gpuLine.SetText("")
	w.inferLine.SetText("")
	w.inferStats.SetText("")
	// Keep the timelines sliding at zero so the outage shows as a dip in the
	// history rather than wiping it.
	w.feedTok(0)
	w.feedNet(0, 0)
	w.feedDisk(0, 0)
	w.coresCap.SetText("[gray]cores")
	w.cores.Reset()
	w.havePrev = false
}

// healthScore derives a 0–100 health figure, folding in the Spark-specific
// thermal and throttle signals on top of host pressure.
func healthScore(s metrics.Snapshot, maxTemp float64, throttling bool) float64 {
	health := 100.0
	switch {
	case s.CPUPct > 90:
		health -= 20
	case s.CPUPct > 75:
		health -= 10
	}
	switch {
	case s.MemUsedPct > memCritPct:
		health -= 30
	case s.MemUsedPct > memWarnPct:
		health -= 15
	}
	switch {
	case s.RootFSPct > 90:
		health -= 20
	case s.RootFSPct > 80:
		health -= 10
	}
	switch {
	case s.Load1 > float64(s.NCPU)*2.0:
		health -= 20
	case s.Load1 > float64(s.NCPU):
		health -= 10
	}
	switch {
	case maxTemp >= tempCritC:
		health -= 25
	case maxTemp >= tempWarnC:
		health -= 10
	}
	if throttling {
		health -= 20
	}
	if health < 0 {
		health = 0
	}
	return health
}

// setCard updates one KPI card's value, sparkline window, and trend arrow.
// goodWhenUp flags metrics where a rising value is benign (e.g. utilization).
// SetSparkline auto-scales to the data max, so the fixed scale is re-applied
// after it — otherwise a quiet card would blow tiny wiggles up to full height.
func setCard(c *components.MetricCard, hist *ring, sparkMax, cur, prev float64, havePrev, goodWhenUp bool) {
	c.SetValue(fmt.Sprintf("%.0f", cur))
	c.SetSparkline(hist.push(cur)).SetSparklineMax(sparkMax)
	tr, delta := components.TrendNeutral, ""
	if havePrev {
		switch d := cur - prev; {
		case d >= 1:
			tr, delta = components.TrendUp, fmt.Sprintf("+%.0f", d)
		case d <= -1:
			tr, delta = components.TrendDown, fmt.Sprintf("%.0f", d)
		}
	}
	c.SetTrend(tr, delta, goodWhenUp)
}

// ---- formatting helpers -------------------------------------------------------

func shortModel(m string) string {
	m = strings.TrimSpace(strings.TrimPrefix(m, "NVIDIA "))
	if m == "" {
		return "GPU"
	}
	return m
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "unreachable"
	}
	return s
}

func humanUptime(d time.Duration) string {
	if d <= 0 {
		return "?"
	}
	days := int(d.Hours()) / 24
	hrs := int(d.Hours()) % 24
	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, hrs)
	}
	if hrs > 0 {
		return fmt.Sprintf("%dh%dm", hrs, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// humanCount renders a rate compactly: 942, 1.2k, 18k.
func humanCount(v float64) string {
	switch {
	case v >= 10000:
		return fmt.Sprintf("%.0fk", v/1000)
	case v >= 1000:
		return fmt.Sprintf("%.1fk", v/1000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// latencyVal renders a windowed latency average with threshold coloring; zero
// means nothing completed that phase this window, shown dim.
func latencyVal(ms, warn, crit float64) string {
	if ms <= 0 {
		return "[gray]–[-]"
	}
	return colorTag(ms, warn, crit) + humanMs(ms) + "[-]"
}

// humanMs renders a millisecond figure compactly: 420ms, 1.4s.
func humanMs(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", ms/1000)
	}
	return fmt.Sprintf("%.0fms", ms)
}

// humanBytes renders a byte rate compactly: 512B, 24K, 1.3M, 2.0G.
func humanBytes(v float64) string {
	switch {
	case v >= 1<<30:
		return fmt.Sprintf("%.1fG", v/(1<<30))
	case v >= 1<<20:
		return fmt.Sprintf("%.1fM", v/(1<<20))
	case v >= 1<<10:
		return fmt.Sprintf("%.0fK", v/(1<<10))
	default:
		return fmt.Sprintf("%.0fB", v)
	}
}

func humanDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.0fs", d.Seconds())
}

func utilTag(p float64) string    { return colorTag(p, 70, 90) }
func tempTagPct(p float64) string { return colorTag(p, tempWarnC, tempCritC) }

// tempTag renders a temperature with a threshold-colored value and °C suffix.
func tempTag(t float64) string {
	if t <= 0 {
		return "[gray]–°C"
	}
	return fmt.Sprintf("%s%.0f°C[-]", tempTagPct(t), t)
}

// colorTag returns an opening color tag for v against warn/crit cutoffs.
func colorTag(v, warn, crit float64) string {
	switch {
	case v >= crit:
		return "[red]"
	case v >= warn:
		return "[yellow]"
	default:
		return "[green]"
	}
}

func healthColor(h float64) tcell.Color {
	switch {
	case h < 50:
		return tcell.ColorRed
	case h < 80:
		return tcell.ColorYellow
	default:
		return tcell.ColorGreen
	}
}

// clamp01 clamps a value between 0 and 1.
func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}
