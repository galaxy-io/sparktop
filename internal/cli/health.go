package cli

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Health probes node_exporter (and dcgm-exporter) reachability on each spec.
//
// Specs are "host" (probes both 9100 and 9400) or "host:port" (probes just that
// port). The "user@host" form is also accepted; the user prefix is stripped.
func Health(args []string) int {
	fs := flag.NewFlagSet("health", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 3*time.Second, "per-check TCP / HTTP timeout")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: sparktop health <host[:port]> [<host[:port]> ...]\n")
		fmt.Fprint(os.Stderr, "  bare host probes both :9100 (node) and :9400 (gpu)\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	specs := fs.Args()
	if len(specs) == 0 {
		fs.Usage()
		return 2
	}

	bad := 0
	for _, spec := range specs {
		host, ports := parseHealthSpec(spec)
		for _, port := range ports {
			if !checkOne(host, port, *timeout) {
				bad++
			}
		}
		fmt.Println()
	}
	if bad > 0 {
		fmt.Fprintf(os.Stderr, "%d check(s) failed.\n", bad)
		return 1
	}
	fmt.Println("All checks passed.")
	return 0
}

func parseHealthSpec(spec string) (host string, ports []int) {
	if i := strings.Index(spec, "@"); i >= 0 {
		spec = spec[i+1:]
	}
	h, p, ok := strings.Cut(spec, ":")
	if !ok {
		return h, []int{9100, 9400}
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return spec, []int{9100, 9400}
	}
	return h, []int{n}
}

func checkOne(host string, port int, timeout time.Duration) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	label := serviceLabel(port)

	fmt.Printf("[%s %s] tcp ", addr, label)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		fmt.Printf("FAIL — %v\n", err)
		return false
	}
	_ = conn.Close()
	fmt.Print("ok  ")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(fmt.Sprintf("http://%s/metrics", addr))
	if err != nil {
		fmt.Printf("/metrics FAIL — %v\n", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("/metrics FAIL — HTTP %s\n", resp.Status)
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	fmt.Printf("/metrics ok — %s\n", summarize(body, port))
	return true
}

func serviceLabel(port int) string {
	switch port {
	case 9100:
		return "node"
	case 9400:
		return "gpu"
	default:
		return ""
	}
}

func summarize(body []byte, port int) string {
	s := string(body)
	if port == 9400 {
		if strings.Contains(s, "DCGM_FI_DEV_GPU_UTIL") {
			return "GPU metrics present"
		}
		return "no DCGM metrics found"
	}
	if strings.Contains(s, "node_cpu_seconds_total") {
		return "node metrics present"
	}
	return "responded"
}
