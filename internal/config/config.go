// Package config loads sparktop's runtime configuration: the list of DGX Spark
// nodes to watch and a few display knobs.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Node is one DGX Spark host running node_exporter (:9100) and dcgm-exporter (:9400).
type Node struct {
	Name     string `yaml:"name"`      // display name; falls back to Host
	Host     string `yaml:"host"`      // hostname or IP, required
	NodePort int    `yaml:"node_port"` // node_exporter port (default 9100)
	GPUPort  int    `yaml:"gpu_port"`  // dcgm-exporter port (default 9400)
	VLLMPort int    `yaml:"vllm_port"` // vLLM /metrics port (e.g. 8000); 0 disables workload scraping
}

// Display returns the name to show for this node.
func (n Node) Display() string {
	if strings.TrimSpace(n.Name) != "" {
		return n.Name
	}
	return n.Host
}

// NodeURL is the node_exporter /metrics endpoint.
func (n Node) NodeURL() string {
	return fmt.Sprintf("http://%s:%d/metrics", n.Host, portOr(n.NodePort, 9100))
}

// GPUURL is the dcgm-exporter /metrics endpoint.
func (n Node) GPUURL() string {
	return fmt.Sprintf("http://%s:%d/metrics", n.Host, portOr(n.GPUPort, 9400))
}

// VLLMURL is the vLLM /metrics endpoint, or "" if no vllm_port is configured.
func (n Node) VLLMURL() string {
	if n.VLLMPort <= 0 {
		return ""
	}
	return fmt.Sprintf("http://%s:%d/metrics", n.Host, n.VLLMPort)
}

func portOr(p, def int) int {
	if p <= 0 {
		return def
	}
	return p
}

// Config is the parsed config file.
type Config struct {
	Interval string `yaml:"interval"` // poll interval, e.g. "2s"
	Timeout  string `yaml:"timeout"`  // per-scrape budget, e.g. "8s" (defaults to 4× interval, min 5s, max 30s)
	History  int    `yaml:"history"`  // points kept per sparkline
	Theme    string `yaml:"theme"`    // dado theme name (optional)
	Nodes    []Node `yaml:"nodes"`
}

// Default returns a Config with sane defaults and no nodes.
func Default() *Config {
	return &Config{Interval: "2s", History: 60}
}

// PollEvery is the validated poll interval.
func (c *Config) PollEvery() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(c.Interval))
	if err != nil || d < 250*time.Millisecond {
		return 2 * time.Second
	}
	return d
}

// ScrapeTimeout is the per-scrape budget, decoupled from the poll cadence so a
// slow exporter doesn't fail just because it's slower than the UI tick. If
// `timeout:` is unset, defaults to max(5s, 4× interval), clamped to 30s.
func (c *Config) ScrapeTimeout() time.Duration {
	if s := strings.TrimSpace(c.Timeout); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			return d
		}
	}
	every := c.PollEvery()
	d := 4 * every
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// HistoryLen is the validated sparkline history length.
func (c *Config) HistoryLen() int {
	if c.History <= 1 {
		return 60
	}
	return c.History
}

// DefaultPath returns the preferred path for a *new* config file:
// $XDG_CONFIG_HOME/sparktop/config.yaml, falling back to
// ~/.config/sparktop/config.yaml. Returns "" if neither $HOME nor
// $XDG_CONFIG_HOME is set.
func DefaultPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "sparktop", "config.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "sparktop", "config.yaml")
}

// SearchPaths returns candidate config paths in lookup order.
// The first one that exists on disk wins. The sparkmon paths are legacy: this
// tool shipped under that name before moving to galaxy-io/sparktop.
func SearchPaths() []string {
	var out []string
	if p := DefaultPath(); p != "" {
		out = append(out, p)
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		out = append(out, filepath.Join(d, "sparkmon", "config.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out,
			filepath.Join(home, ".sparktop", "config.yaml"),
			filepath.Join(home, ".config", "sparkmon", "config.yaml"),
			filepath.Join(home, ".sparkmon", "config.yaml"),
		)
	}
	out = append(out, "config.yaml")
	return out
}

// Resolve picks the first existing path in SearchPaths(), or returns "" if
// none exist.
func Resolve() string {
	for _, p := range SearchPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(c.Interval) == "" {
		c.Interval = "2s"
	}
	for i := range c.Nodes {
		if strings.TrimSpace(c.Nodes[i].Host) == "" {
			return nil, fmt.Errorf("%s: node %d is missing 'host'", path, i+1)
		}
	}
	return c, nil
}

// ParseNodesFlag parses a comma-separated "name=host" list from the -nodes flag.
// "host" alone (no "=") is allowed; the host doubles as the name.
func ParseNodesFlag(s string) ([]Node, error) {
	var out []Node
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, host, ok := strings.Cut(part, "=")
		if !ok {
			host, name = part, ""
		}
		host = strings.TrimSpace(host)
		if host == "" {
			return nil, fmt.Errorf("bad node spec %q (want name=host)", part)
		}
		out = append(out, Node{Name: strings.TrimSpace(name), Host: host})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no nodes parsed from %q", s)
	}
	return out, nil
}
