package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVLLMRoles(t *testing.T) {
	worker := Node{Host: "worker", VLLMPort: 8000, VLLMRole: " WORKER "}
	if got := worker.NormalizedVLLMRole(); got != VLLMRoleWorker {
		t.Fatalf("NormalizedVLLMRole() = %q", got)
	}
	if got := worker.VLLMURL(); got != "" {
		t.Fatalf("worker VLLMURL() = %q, want empty", got)
	}

	server := Node{Host: "server", VLLMPort: 8000, VLLMRole: VLLMRoleServer}
	if got := server.VLLMURL(); got != "http://server:8000/metrics" {
		t.Fatalf("server VLLMURL() = %q", got)
	}
}

func TestLoadRejectsInvalidVLLMRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := os.WriteFile(path, []byte("nodes:\n  - host: spark-01\n    vllm_role: leader\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "invalid vllm_role") {
		t.Fatalf("Load() error = %v, want invalid vllm_role", err)
	}
}
