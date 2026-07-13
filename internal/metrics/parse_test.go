package metrics

import (
	"strings"
	"testing"
)

const sampleExposition = `# HELP node_cpu_seconds_total Seconds the CPUs spent in each mode.
# TYPE node_cpu_seconds_total counter
node_cpu_seconds_total{cpu="0",mode="idle"} 1234.5
node_cpu_seconds_total{cpu="0",mode="user"} 10
node_cpu_seconds_total{cpu="1",mode="idle"} 2000
node_load1 0.42
node_memory_MemTotal_bytes 1.34217728e+11
node_memory_MemAvailable_bytes 1e+11
node_filesystem_avail_bytes{device="/dev/nvme0n1p2",fstype="ext4",mountpoint="/"} 5e+09
node_filesystem_size_bytes{device="/dev/nvme0n1p2",fstype="ext4",mountpoint="/"} 1e+10
node_network_receive_bytes_total{device="eth0"} 1000
node_network_receive_bytes_total{device="docker0"} 999999
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="NVIDIA GB10",Hostname="spark-01"} 42
DCGM_FI_DEV_FB_USED{gpu="0",modelName="NVIDIA GB10"} 18000
DCGM_FI_DEV_FB_FREE{gpu="0",modelName="NVIDIA GB10"} 100000
DCGM_FI_DEV_GPU_TEMP{gpu="0",modelName="NVIDIA GB10"} 61
`

func TestParseText(t *testing.T) {
	f, err := parseText(strings.NewReader(sampleExposition))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := f.sum("node_cpu_seconds_total"), 1234.5+10+2000; got != want {
		t.Errorf("cpu seconds sum = %v, want %v", got, want)
	}
	idle := f.sumWhere("node_cpu_seconds_total", func(l map[string]string) bool { return l["mode"] == "idle" })
	if idle != 3234.5 {
		t.Errorf("idle seconds = %v, want 3234.5", idle)
	}
	if v, ok := f.first("node_load1"); !ok || v != 0.42 {
		t.Errorf("load1 = %v, %v", v, ok)
	}

	// only eth0 should count as a physical interface
	if got := f.sumWhere("node_network_receive_bytes_total", isPhysicalIface); got != 1000 {
		t.Errorf("physical rx = %v, want 1000", got)
	}
}

func TestParseGPUs(t *testing.T) {
	f, _ := parseText(strings.NewReader(sampleExposition))
	gpus := parseGPUs(f)
	if len(gpus) != 1 {
		t.Fatalf("got %d gpus, want 1", len(gpus))
	}
	g := gpus[0]
	if g.Index != "0" || g.Model != "NVIDIA GB10" {
		t.Errorf("gpu identity = %+v", g)
	}
	if g.UtilPct != 42 || g.TempC != 61 {
		t.Errorf("gpu metrics = %+v", g)
	}
	if g.MemUsedB != 18000*1024*1024 {
		t.Errorf("mem used = %v", g.MemUsedB)
	}
	if g.MemTotalB != (18000+100000)*1024*1024 {
		t.Errorf("mem total = %v (want used+free)", g.MemTotalB)
	}
}

func TestPhysicalFilters(t *testing.T) {
	for _, d := range []string{"eth0", "enp4s0", "wlan0", "ib0"} {
		if !isPhysicalIface(map[string]string{"device": d}) {
			t.Errorf("%s should be physical", d)
		}
	}
	for _, d := range []string{"lo", "docker0", "veth123", "br-abc", "virbr0"} {
		if isPhysicalIface(map[string]string{"device": d}) {
			t.Errorf("%s should be excluded", d)
		}
	}
	if !isPhysicalDisk(map[string]string{"device": "nvme0n1"}) || isPhysicalDisk(map[string]string{"device": "loop0"}) {
		t.Error("disk filter wrong")
	}
}
