package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/atterpac/dado/theme"
	"github.com/atterpac/dado/theme/themes"
	"github.com/galaxy-io/sparktop/internal/config"
	"github.com/galaxy-io/sparktop/internal/metrics"
	"github.com/galaxy-io/sparktop/internal/ui"
)

// Dashboard runs the live cluster dashboard.
func Dashboard(args []string) int {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "path to config file (defaults to the first existing of: $XDG_CONFIG_HOME/sparktop/config.yaml, ~/.config/sparktop/config.yaml, ~/.sparktop/config.yaml, ./config.yaml)")
	nodesFlag := fs.String("nodes", "", "comma-separated name=host pairs (overrides config nodes), e.g. spark-01=10.0.0.1,spark-02=10.0.0.2")
	intervalFlag := fs.Duration("interval", 0, "poll interval (overrides config)")
	themeNameFlag := fs.String("theme", "", "theme name (overrides config file)")
	vllmPortFlag := fs.Int("vllm-port", 0, "scrape vLLM /metrics on this port for every node (e.g. 8000); 0 disables")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if err := runDashboard(*cfgPath, *nodesFlag, *intervalFlag, *themeNameFlag, *vllmPortFlag); err != nil {
		fmt.Fprintln(os.Stderr, "sparktop:", err)
		return 1
	}
	return 0
}

func runDashboard(cfgPath, nodesFlag string, intervalFlag time.Duration, themeName string, vllmPort int) error {
	resolved := cfgPath
	if resolved == "" {
		resolved = config.Resolve()
	}

	var cfg *config.Config
	if resolved != "" {
		c, err := config.Load(resolved)
		if err != nil {
			return err
		}
		cfg = c
	} else {
		if nodesFlag == "" {
			return fmt.Errorf("no config file found in %v\n(create one at %s, or pass -config <file> or -nodes name=host,…)",
				config.SearchPaths(), config.DefaultPath())
		}
		cfg = config.Default()
	}
	if nodesFlag != "" {
		ns, err := config.ParseNodesFlag(nodesFlag)
		if err != nil {
			return fmt.Errorf("-nodes: %w", err)
		}
		cfg.Nodes = ns
	}
	if intervalFlag > 0 {
		cfg.Interval = intervalFlag.String()
	}
	if len(cfg.Nodes) == 0 {
		return fmt.Errorf("no nodes configured")
	}
	// --vllm-port applies to every node, which is what you want with --nodes
	// (that flag can't carry a per-node port). For per-node ports, use a config
	// file with `vllm_port:` on each node instead.
	if vllmPort > 0 {
		for i := range cfg.Nodes {
			cfg.Nodes[i].VLLMPort = vllmPort
		}
	}

	chosen := cfg.Theme
	if themeName != "" {
		chosen = themeName
	}
	if chosen == "" {
		chosen = themes.DefaultName
	}
	selected := themes.Get(chosen)
	if selected == nil {
		fmt.Fprintf(os.Stderr, "Warning: theme %q not found, using default\n", chosen)
		chosen = themes.DefaultName
		selected = themes.Default()
	}
	theme.SetProvider(selected)
	// Record the resolved name so the UI's `t` key cycles from the right spot.
	cfg.Theme = chosen

	every := cfg.PollEvery()
	timeout := cfg.ScrapeTimeout()
	coll := metrics.NewCollector(cfg.Nodes, timeout)
	app := ui.New(cfg, cfg.Nodes)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	force := make(chan struct{}, 1)
	app.SetOnForceRefresh(func() {
		select {
		case force <- struct{}{}:
		default:
		}
	})

	go func() {
		poll := func() {
			cctx, c := context.WithTimeout(ctx, timeout)
			snaps := coll.Collect(cctx)
			c()
			app.Submit(snaps)
		}
		poll()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				poll()
			case <-force:
				poll()
			}
		}
	}()

	return app.Run()
}
