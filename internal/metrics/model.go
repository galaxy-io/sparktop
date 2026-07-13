package metrics

import "time"

// GPU is one GPU as reported by dcgm-exporter.
type GPU struct {
	Index      string  // the "gpu" label, e.g. "0"
	Model      string  // the "modelName" label, e.g. "NVIDIA GB10"
	UtilPct    float64 // 0..100
	MemUsedB   float64 // framebuffer bytes in use
	MemTotalB  float64 // total framebuffer bytes (used+free, or FB_TOTAL if exported)
	TempC      float64
	PowerW     float64
	SMClockMHz float64
}

// Snapshot is a point-in-time view of one node.
type Snapshot struct {
	Name string
	When time.Time

	// reachability
	Up     bool   // node_exporter responded
	GPUUp  bool   // dcgm-exporter responded
	Err    string // node_exporter scrape error, if any
	GPUErr string // dcgm-exporter scrape error, if any

	// host metrics (from node_exporter)
	NCPU        int
	CPUPct      float64   // 0..100, derived from cpu-seconds deltas
	PerCoreUtil []float64 // 0..100 per logical CPU, indexed in cpu-label order
	Load1       float64
	MemTotalB   float64
	MemUsedB    float64
	MemUsedPct  float64 // 0..100
	RootFSPct   float64 // 0..100, "/" filesystem
	Uptime      time.Duration
	NetRxBps    float64 // sum over physical interfaces, derived
	NetTxBps    float64
	DiskRBps    float64 // sum over physical disks, derived
	DiskWBps    float64

	// GPU metrics (from dcgm-exporter)
	GPUs []GPU

	// inference workload (from vLLM /metrics); only populated when a node has
	// vllm_port set.
	InferOn         bool    // a vLLM endpoint is configured for this node
	InferUp         bool    // vLLM /metrics responded
	InferErr        string  // vLLM scrape error, if any
	InferModel      string  // served model name
	ReqRunning      float64 // requests currently decoding
	ReqWaiting      float64 // requests queued (backpressure when > 0)
	KVCachePct      float64 // 0..100, GPU KV-cache occupancy
	GenTokPerSec    float64 // generation (decode) throughput, derived from the counter
	PromptTokPerSec float64 // prompt (prefill) throughput, derived from the counter
	TTFTMs          float64 // avg time-to-first-token this window, ms (0 = no requests started)
	ITLMs           float64 // avg inter-token latency this window, ms (0 = no decode steps)
	Preemptions     float64 // requests preempted this window (KV-cache pressure tell)
}

// UpGPUs is the number of GPUs reported across the node.
func (s Snapshot) UpGPUs() int { return len(s.GPUs) }
