package slurm

import (
	"os"
	"testing"
	"time"
)

func TestLiveSlurmJSONDiagnostic(t *testing.T) {
	nodesPath := os.Getenv("OPENHPC_DIAGNOSTIC_SINFO")
	jobsPath := os.Getenv("OPENHPC_DIAGNOSTIC_SQUEUE")
	if nodesPath == "" || jobsPath == "" {
		t.Skip("live diagnostic paths are not configured")
	}
	nodes, err := os.ReadFile(nodesPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseNodesJSON(nodes); err != nil {
		t.Fatalf("parse nodes: %v", err)
	}
	jobs, err := os.ReadFile(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseJobsJSON(jobs, time.Now()); err != nil {
		t.Fatalf("parse jobs: %v", err)
	}
}
