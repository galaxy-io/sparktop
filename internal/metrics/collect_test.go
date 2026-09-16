package metrics

import (
	"testing"

	"github.com/galaxy-io/sparktop/internal/config"
)

func TestReconcileInferenceDetectsClusterWorker(t *testing.T) {
	c := &Collector{states: []*nodeState{
		{cfg: config.Node{Name: "server", VLLMPort: 8000}},
		{cfg: config.Node{Name: "worker", VLLMPort: 8000}},
	}}
	snaps := []Snapshot{
		{Name: "server", InferOn: true, InferUp: true, InferModel: "model-a"},
		{Name: "worker", InferOn: true, InferErr: "dial tcp 10.0.0.2:8000: connect: connection refused", GPUs: []GPU{{UtilPct: 95}}},
	}

	c.reconcileInference(snaps)

	if !snaps[1].InferWorker {
		t.Fatal("busy connection-refused peer was not recognized as a worker")
	}
	if snaps[1].InferPeer != "server" || snaps[1].InferModel != "model-a" {
		t.Fatalf("worker metadata = peer %q, model %q", snaps[1].InferPeer, snaps[1].InferModel)
	}

	// Once positively identified, the worker remains labeled during idle
	// samples as long as its endpoint still refuses connections.
	snaps[1] = Snapshot{Name: "worker", InferOn: true, InferErr: "connection refused", GPUs: []GPU{{UtilPct: 0}}}
	c.reconcileInference(snaps)
	if !snaps[1].InferWorker {
		t.Fatal("detected worker flickered back to a failed endpoint while idle")
	}
}

func TestReconcileInferenceLeavesIdleAutoNodeFailed(t *testing.T) {
	c := &Collector{states: []*nodeState{
		{cfg: config.Node{Name: "server", VLLMPort: 8000}},
		{cfg: config.Node{Name: "peer", VLLMPort: 8000}},
	}}
	snaps := []Snapshot{
		{Name: "server", InferOn: true, InferUp: true},
		{Name: "peer", InferOn: true, InferErr: "connection refused", GPUs: []GPU{{UtilPct: 0}}},
	}

	c.reconcileInference(snaps)

	if snaps[1].InferWorker {
		t.Fatal("idle auto node was incorrectly classified as a worker")
	}
}

func TestReconcileInferenceDetectsUnconfiguredBusyPeer(t *testing.T) {
	c := &Collector{states: []*nodeState{
		{cfg: config.Node{Name: "server", VLLMPort: 8000}},
		{cfg: config.Node{Name: "worker"}},
	}}
	snaps := []Snapshot{
		{Name: "server", InferOn: true, InferUp: true, InferModel: "model-a", ReqRunning: 2},
		{Name: "worker", GPUs: []GPU{{UtilPct: 96}}},
	}

	c.reconcileInference(snaps)

	if !snaps[1].InferOn || !snaps[1].InferWorker || snaps[1].InferPeer != "server" {
		t.Fatalf("unconfigured peer was not recognized: %+v", snaps[1])
	}
}

func TestReconcileInferenceAttachesExplicitIdleWorker(t *testing.T) {
	c := &Collector{states: []*nodeState{
		{cfg: config.Node{Name: "server", VLLMPort: 8000, VLLMRole: config.VLLMRoleServer}},
		{cfg: config.Node{Name: "worker", VLLMRole: config.VLLMRoleWorker}},
	}}
	snaps := []Snapshot{
		{Name: "server", InferOn: true, InferUp: true, InferModel: "model-a"},
		{Name: "worker", InferOn: true, InferWorker: true},
	}

	c.reconcileInference(snaps)

	if snaps[1].InferPeer != "server" || snaps[1].InferModel != "model-a" {
		t.Fatalf("explicit worker metadata = peer %q, model %q", snaps[1].InferPeer, snaps[1].InferModel)
	}
}
