package ui

import (
	"strings"
	"testing"

	"github.com/galaxy-io/sparktop/internal/config"
	"github.com/galaxy-io/sparktop/internal/metrics"
	"github.com/gdamore/tcell/v2"
)

func TestClusterInferenceUsesSharedThroughputPane(t *testing.T) {
	cfg := config.Default()
	nodes := []config.Node{{Name: "server"}, {Name: "worker"}}
	u := New(cfg, nodes)
	u.render([]metrics.Snapshot{
		{Name: "server", Up: true, InferOn: true, InferUp: true, InferModel: "model-a", ReqRunning: 2, GenTokPerSec: 74, PromptTokPerSec: 111},
		{Name: "worker", Up: true, InferOn: true, InferWorker: true, InferPeer: "server", InferModel: "model-a"},
	})

	if !u.clusterTokVisible {
		t.Fatal("shared throughput pane is hidden")
	}
	if got := u.clusterTok.values[len(u.clusterTok.values)-1]; got != 74 {
		t.Fatalf("shared throughput = %v, want 74", got)
	}
	if u.nodes[0].inferRows != 4 || u.nodes[1].inferRows != 4 {
		t.Fatalf("inference cards are not aligned: server=%d worker=%d",
			u.nodes[0].inferRows, u.nodes[1].inferRows)
	}
	if got := u.nodes[1].inferStats.GetText(); !strings.Contains(got, "READY") || !strings.Contains(got, "(via server)") {
		t.Fatalf("worker status = %q", got)
	}

	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatal(err)
	}
	defer screen.Fini()
	screen.SetSize(200, 60)
	u.content.SetRect(0, 0, 200, 60)
	u.content.Draw(screen)

	_, serverCoresY, _, _ := u.nodes[0].coresBox.Rect()
	_, workerCoresY, _, _ := u.nodes[1].coresBox.Rect()
	if serverCoresY != workerCoresY {
		t.Fatalf("core metrics are misaligned: server y=%d, worker y=%d", serverCoresY, workerCoresY)
	}
	_, throughputY, _, throughputH := u.clusterTokPanel.Rect()
	if throughputY != 51 || throughputH != 9 {
		t.Fatalf("shared throughput pane rect = y:%d h:%d, want y:51 h:9", throughputY, throughputH)
	}
}
