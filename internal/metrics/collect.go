package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/galaxy-io/sparktop/internal/config"
)

// nodeState tracks one node's previous counters so we can turn them into rates.
type nodeState struct {
	cfg config.Node

	havePrev  bool
	prevTime  time.Time
	cpuTotal  float64
	cpuIdle   float64
	coreTotal map[string]float64 // per-cpu sum across modes
	coreIdle  map[string]float64 // per-cpu idle counter
	netRx     float64
	netTx     float64
	diskR     float64
	diskW     float64

	// vLLM counter state for throughput rates and windowed latency averages
	vllmHavePrev  bool
	vllmPrevTime  time.Time
	prevGenTok    float64
	prevPromptTok float64
	prevTTFTSum   float64
	prevTTFTCount float64
	prevITLSum    float64
	prevITLCount  float64
	prevPreempt   float64
}

// Collector scrapes a fixed set of nodes.
type Collector struct {
	client *http.Client
	states []*nodeState
}

// NewCollector builds a Collector for the given nodes. timeout bounds each HTTP
// scrape.
func NewCollector(nodes []config.Node, timeout time.Duration) *Collector {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	c := &Collector{client: &http.Client{Timeout: timeout}}
	for _, n := range nodes {
		c.states = append(c.states, &nodeState{cfg: n})
	}
	return c
}

// Collect scrapes every node concurrently and returns one Snapshot per node, in
// configuration order.
func (c *Collector) Collect(ctx context.Context) []Snapshot {
	out := make([]Snapshot, len(c.states))
	var wg sync.WaitGroup
	for i := range c.states {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = c.collectOne(ctx, c.states[i])
		}(i)
	}
	wg.Wait()
	return out
}

func (c *Collector) collectOne(ctx context.Context, st *nodeState) Snapshot {
	s := Snapshot{Name: st.cfg.Display(), When: time.Now()}

	if hf, err := c.scrape(ctx, st.cfg.NodeURL()); err != nil {
		s.Err = err.Error()
	} else {
		s.Up = true
		now := time.Now()
		fillHost(&s, hf)

		cpuTotal := hf.sum("node_cpu_seconds_total")
		cpuIdle := hf.sumWhere("node_cpu_seconds_total", func(l map[string]string) bool { return l["mode"] == "idle" })
		coreTotal, coreIdle := perCoreCPU(hf)
		netRx := hf.sumWhere("node_network_receive_bytes_total", isPhysicalIface)
		netTx := hf.sumWhere("node_network_transmit_bytes_total", isPhysicalIface)
		diskR := hf.sumWhere("node_disk_read_bytes_total", isPhysicalDisk)
		diskW := hf.sumWhere("node_disk_written_bytes_total", isPhysicalDisk)

		if st.havePrev {
			if dt := now.Sub(st.prevTime).Seconds(); dt > 0 {
				if dTotal := cpuTotal - st.cpuTotal; dTotal > 0 {
					s.CPUPct = clampf(100*(1-(cpuIdle-st.cpuIdle)/dTotal), 0, 100)
				}
				s.PerCoreUtil = perCoreUtil(coreTotal, coreIdle, st.coreTotal, st.coreIdle)
				s.NetRxBps = nonneg(netRx-st.netRx) / dt
				s.NetTxBps = nonneg(netTx-st.netTx) / dt
				s.DiskRBps = nonneg(diskR-st.diskR) / dt
				s.DiskWBps = nonneg(diskW-st.diskW) / dt
			}
		}
		st.havePrev = true
		st.prevTime = now
		st.cpuTotal, st.cpuIdle = cpuTotal, cpuIdle
		st.coreTotal, st.coreIdle = coreTotal, coreIdle
		st.netRx, st.netTx = netRx, netTx
		st.diskR, st.diskW = diskR, diskW
	}

	if gf, err := c.scrape(ctx, st.cfg.GPUURL()); err != nil {
		s.GPUErr = err.Error()
	} else {
		s.GPUUp = true
		s.GPUs = parseGPUs(gf)
	}

	if url := st.cfg.VLLMURL(); url != "" {
		s.InferOn = true
		if vf, err := c.scrape(ctx, url); err != nil {
			s.InferErr = err.Error()
		} else {
			s.InferUp = true
			fillInfer(&s, vf, st, time.Now())
		}
	}
	return s
}

// fillInfer reads vLLM's Prometheus metrics. The token counters are turned into
// per-second rates the same way host counters are.
func fillInfer(s *Snapshot, f family, st *nodeState, now time.Time) {
	s.ReqRunning = f.sum("vllm:num_requests_running")
	s.ReqWaiting = f.sum("vllm:num_requests_waiting")
	// vLLM renamed the KV-cache gauge; accept both spellings.
	if v, ok := f.first("vllm:gpu_cache_usage_perc"); ok {
		s.KVCachePct = clampf(v*100, 0, 100)
	} else if v, ok := f.first("vllm:kv_cache_usage_perc"); ok {
		s.KVCachePct = clampf(v*100, 0, 100)
	}
	s.InferModel = vllmModel(f)

	gen := f.sum("vllm:generation_tokens_total")
	prompt := f.sum("vllm:prompt_tokens_total")
	ttftSum := f.sum("vllm:time_to_first_token_seconds_sum")
	ttftCount := f.sum("vllm:time_to_first_token_seconds_count")
	itlSum := f.sum("vllm:time_per_output_token_seconds_sum")
	itlCount := f.sum("vllm:time_per_output_token_seconds_count")
	if itlCount == 0 { // newer vLLM builds rename the histogram
		itlSum = f.sum("vllm:inter_token_latency_seconds_sum")
		itlCount = f.sum("vllm:inter_token_latency_seconds_count")
	}
	preempt := f.sum("vllm:num_preemptions_total")

	if st.vllmHavePrev {
		if dt := now.Sub(st.vllmPrevTime).Seconds(); dt > 0 {
			s.GenTokPerSec = nonneg(gen-st.prevGenTok) / dt
			s.PromptTokPerSec = nonneg(prompt-st.prevPromptTok) / dt
		}
		// Histogram sum/count deltas give the true average over the window;
		// a zero count means nothing completed that phase, shown as "–".
		if dc := ttftCount - st.prevTTFTCount; dc > 0 {
			s.TTFTMs = nonneg(ttftSum-st.prevTTFTSum) / dc * 1000
		}
		if dc := itlCount - st.prevITLCount; dc > 0 {
			s.ITLMs = nonneg(itlSum-st.prevITLSum) / dc * 1000
		}
		s.Preemptions = nonneg(preempt - st.prevPreempt)
	}
	st.vllmHavePrev = true
	st.vllmPrevTime = now
	st.prevGenTok = gen
	st.prevPromptTok = prompt
	st.prevTTFTSum, st.prevTTFTCount = ttftSum, ttftCount
	st.prevITLSum, st.prevITLCount = itlSum, itlCount
	st.prevPreempt = preempt
}

// vllmModel pulls the served model name from the "model_name" label that vLLM
// attaches to its metrics.
func vllmModel(f family) string {
	for _, name := range []string{"vllm:num_requests_running", "vllm:generation_tokens_total", "vllm:num_requests_waiting"} {
		for _, sm := range f[name] {
			if m := sm.Labels["model_name"]; m != "" {
				return m
			}
		}
	}
	return ""
}

func (c *Collector) scrape(ctx context.Context, url string) (family, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, cleanErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	return parseText(resp.Body)
}

// perCoreCPU walks node_cpu_seconds_total samples once, grouping by the "cpu"
// label. Returns (total seconds, idle seconds) per core.
func perCoreCPU(f family) (total, idle map[string]float64) {
	total = map[string]float64{}
	idle = map[string]float64{}
	for _, sm := range f["node_cpu_seconds_total"] {
		cpu := sm.Labels["cpu"]
		total[cpu] += sm.Value
		if sm.Labels["mode"] == "idle" {
			idle[cpu] = sm.Value
		}
	}
	return total, idle
}

// perCoreUtil computes per-core utilization (%) from current and previous
// counters. Results are ordered numerically by the "cpu" label.
func perCoreUtil(total, idle, prevTotal, prevIdle map[string]float64) []float64 {
	if len(prevTotal) == 0 {
		return nil
	}
	keys := make([]string, 0, len(total))
	for k := range total {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return lessIndex(keys[i], keys[j]) })

	out := make([]float64, 0, len(keys))
	for _, k := range keys {
		dt := total[k] - prevTotal[k]
		if dt <= 0 {
			out = append(out, 0)
			continue
		}
		di := idle[k] - prevIdle[k]
		out = append(out, clampf(100*(1-di/dt), 0, 100))
	}
	return out
}

func fillHost(s *Snapshot, f family) {
	cpus := map[string]struct{}{}
	for _, sm := range f["node_cpu_seconds_total"] {
		cpus[sm.Labels["cpu"]] = struct{}{}
	}
	s.NCPU = len(cpus)

	s.Load1, _ = f.first("node_load1")

	memTotal, _ := f.first("node_memory_MemTotal_bytes")
	memAvail, _ := f.first("node_memory_MemAvailable_bytes")
	s.MemTotalB = memTotal
	if memTotal > 0 {
		s.MemUsedB = memTotal - memAvail
		s.MemUsedPct = clampf(100*s.MemUsedB/memTotal, 0, 100)
	}

	if bt, ok := f.first("node_boot_time_seconds"); ok {
		if nt, ok2 := f.first("node_time_seconds"); ok2 && nt > bt {
			s.Uptime = time.Duration(nt-bt) * time.Second
		}
	}

	var rootSize, rootAvail float64
	for _, sm := range f["node_filesystem_size_bytes"] {
		if sm.Labels["mountpoint"] == "/" {
			rootSize = sm.Value
		}
	}
	for _, sm := range f["node_filesystem_avail_bytes"] {
		if sm.Labels["mountpoint"] == "/" {
			rootAvail = sm.Value
		}
	}
	if rootSize > 0 {
		s.RootFSPct = clampf(100*(1-rootAvail/rootSize), 0, 100)
	}
}

func parseGPUs(f family) []GPU {
	byIdx := map[string]*GPU{}
	get := func(l map[string]string) *GPU {
		id := l["gpu"]
		g := byIdx[id]
		if g == nil {
			g = &GPU{Index: id}
			byIdx[id] = g
		}
		if g.Model == "" {
			if m := l["modelName"]; m != "" {
				g.Model = m
			}
		}
		return g
	}
	for _, sm := range f["DCGM_FI_DEV_GPU_UTIL"] {
		get(sm.Labels).UtilPct = sm.Value
	}
	for _, sm := range f["DCGM_FI_DEV_FB_USED"] {
		get(sm.Labels).MemUsedB = sm.Value * 1024 * 1024 // MiB -> bytes
	}
	for _, sm := range f["DCGM_FI_DEV_FB_TOTAL"] {
		get(sm.Labels).MemTotalB = sm.Value * 1024 * 1024
	}
	for _, sm := range f["DCGM_FI_DEV_GPU_TEMP"] {
		get(sm.Labels).TempC = sm.Value
	}
	for _, sm := range f["DCGM_FI_DEV_POWER_USAGE"] {
		get(sm.Labels).PowerW = sm.Value
	}
	for _, sm := range f["DCGM_FI_DEV_SM_CLOCK"] {
		get(sm.Labels).SMClockMHz = sm.Value
	}
	free := map[string]float64{}
	for _, sm := range f["DCGM_FI_DEV_FB_FREE"] {
		free[sm.Labels["gpu"]] += sm.Value * 1024 * 1024
	}
	for id, g := range byIdx {
		if g.MemTotalB == 0 {
			g.MemTotalB = g.MemUsedB + free[id]
		}
	}

	out := make([]GPU, 0, len(byIdx))
	for _, g := range byIdx {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return lessIndex(out[i].Index, out[j].Index) })
	return out
}

// lessIndex orders GPU index labels numerically when possible ("2" < "10").
func lessIndex(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func isPhysicalIface(l map[string]string) bool {
	d := l["device"]
	if d == "" || d == "lo" {
		return false
	}
	for _, p := range []string{"docker", "veth", "br-", "virbr", "cni", "flannel", "tap", "tun", "kube"} {
		if strings.HasPrefix(d, p) {
			return false
		}
	}
	return true
}

func isPhysicalDisk(l map[string]string) bool {
	d := l["device"]
	if d == "" {
		return false
	}
	for _, p := range []string{"loop", "ram", "dm-", "sr", "fd", "md"} {
		if strings.HasPrefix(d, p) {
			return false
		}
	}
	return true
}

func clampf(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func nonneg(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}

// cleanErr trims the noisy "Get \"url\":" prefix from net/http errors.
func cleanErr(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 && strings.HasPrefix(msg, "Get ") {
		return fmt.Errorf("%s", strings.TrimSpace(msg[i+1:]))
	}
	return err
}
