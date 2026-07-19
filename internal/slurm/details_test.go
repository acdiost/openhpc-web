package slurm

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func TestClientNodesParsesSinfoJSON(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(`{
		"errors": [], "warnings": [],
		"sinfo": [
			{"nodes":{"nodes":["node31"]},"partition":{"name":"GPU"},"node":{"state":["ALLOCATED"]},"cpus":{"allocated":48,"total":128},"memory":{"maximum":510000},"gres":{"total":"gpu:rtx_3090:8"}},
			{"nodes":{"nodes":["node32"]},"partition":{"name":"GPU"},"node":{"state":["IDLE"]},"cpus":{"allocated":0,"total":128},"memory":{"maximum":510000},"gres":{"total":""}},
			{"nodes":{"nodes":["node33"]},"partition":{"name":"GPU"},"node":{"state":["IDLE","NO_RESPOND"]},"cpus":{"allocated":0,"total":128},"memory":{"maximum":510000},"gres":{"total":"gpu:rtx_3090:8"}}
		]}`)}}
	nodes, err := newTestClient(t, runner).Nodes(context.Background())
	if err != nil {
		t.Fatalf("Nodes() error = %v", err)
	}
	want := []cluster.Node{
		{Name: "node31", Partition: "GPU", State: "allocated", AllocatedCPUs: 48, TotalCPUs: 128, MemoryMB: 510000, GRES: "gpu:rtx_3090:8", Online: true},
		{Name: "node32", Partition: "GPU", State: "idle", TotalCPUs: 128, MemoryMB: 510000, Online: true},
		{Name: "node33", Partition: "GPU", State: "idle", TotalCPUs: 128, MemoryMB: 510000, GRES: "gpu:rtx_3090:8"},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Errorf("Nodes() = %#v, want %#v", nodes, want)
	}
	wantCall := []commandCall{{path: filepath.Join("/opt/slurm/bin", "sinfo"), args: []string{"--Node", "--json"}}}
	if calls := runner.callsSnapshot(); !reflect.DeepEqual(calls, wantCall) {
		t.Errorf("runner calls = %#v, want %#v", calls, wantCall)
	}
}

func TestClientJobsParsesSqueueJSON(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"squeue": []byte(`{
		"errors": [], "warnings": [],
		"jobs": [
			{"job_id":32940,"name":"simulation|phase2","user_name":"liyuxiang","user_id":{"set":true,"number":10001},"account":"jfzx","partition":"GPU","job_state":["RUNNING"],"cpus":{"set":true,"number":16},"submit_time":{"set":true,"number":1704060000},"eligible_time":{"set":true,"number":1704063600},"start_time":{"set":true,"infinite":false,"number":1704067200},"end_time":{"set":false,"number":0},"time_limit":{"set":false,"infinite":true,"number":0},"node_count":{"set":true,"infinite":false,"number":1},"nodes":"node31","state_reason":"None","current_working_directory":"/home/liyuxiang/work/PVA-MPN/20","standard_output":"/home/liyuxiang/work/PVA-MPN/20/out_32940.log","standard_error":"/home/liyuxiang/work/PVA-MPN/20/err_32940.log","command":"/home/liyuxiang/work/PVA-MPN/20/md_gpu.slurm"},
			{"job_id":32941,"name":"wait","user_name":"user1","account":"jfzx","job_state":["PENDING"],"start_time":{"set":false,"infinite":false,"number":0},"time_limit":{"set":true,"infinite":false,"number":60},"node_count":{"set":true,"infinite":false,"number":2},"nodes":"","state_reason":"Resources"}
		]}`)}}
	client := newTestClient(t, runner)
	client.now = func() time.Time { return time.Unix(1704070800, 0).UTC() }
	jobs, err := client.Jobs(context.Background())
	if err != nil {
		t.Fatalf("Jobs() error = %v", err)
	}
	want := []cluster.Job{
		{ID: "32940", Name: "simulation|phase2", User: "liyuxiang", UserID: 10001, Account: "jfzx", Partition: "GPU", State: "RUNNING", CPUCount: 16, Elapsed: "1:00:00", TimeLimit: "UNLIMITED", NodeCount: 1, Nodes: "node31", NodesOrReason: "node31", SubmitTime: "2023-12-31T22:00:00", EligibleTime: "2023-12-31T23:00:00", StartTime: "2024-01-01T00:00:00", EndTime: "Unknown", WorkDir: "/home/liyuxiang/work/PVA-MPN/20", StdOut: "/home/liyuxiang/work/PVA-MPN/20/out_32940.log", StdErr: "/home/liyuxiang/work/PVA-MPN/20/err_32940.log", Command: "/home/liyuxiang/work/PVA-MPN/20/md_gpu.slurm"},
		{ID: "32941", Name: "wait", User: "user1", Account: "jfzx", State: "PENDING", Elapsed: "—", TimeLimit: "1:00:00", NodeCount: 2, Nodes: "—", NodesOrReason: "Resources", SubmitTime: "—", EligibleTime: "—", StartTime: "—", EndTime: "Unknown", WorkDir: "—", StdOut: "—", StdErr: "—", Command: "—"},
	}
	if !reflect.DeepEqual(jobs, want) {
		t.Errorf("Jobs() = %#v, want %#v", jobs, want)
	}
	wantCall := []commandCall{{path: filepath.Join("/opt/slurm/bin", "squeue"), args: []string{"--json"}}}
	if calls := runner.callsSnapshot(); !reflect.DeepEqual(calls, wantCall) {
		t.Errorf("runner calls = %#v, want %#v", calls, wantCall)
	}
}

func TestClientJobFindsJobFromCachedSnapshot(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"squeue": []byte(`{
		"errors": [],
		"jobs": [{"job_id":32940,"name":"train-model","user_name":"user","account":"research","job_state":["RUNNING"],"node_count":{"set":true,"number":1},"nodes":"node31"}]
	}`)}}
	client := newTestClient(t, runner)

	job, found, err := client.Job(context.Background(), 32940)
	if err != nil {
		t.Fatalf("Job() error = %v", err)
	}
	if !found || job.ID != "32940" || job.Name != "train-model" {
		t.Errorf("Job() = (%#v, %v), want matching job", job, found)
	}
	_, found, err = client.Job(context.Background(), 32941)
	if err != nil {
		t.Fatalf("Job(missing) error = %v", err)
	}
	if found {
		t.Error("Job(missing) found = true, want false")
	}
	wantCalls := []commandCall{{path: filepath.Join("/opt/slurm/bin", "squeue"), args: []string{"--json"}}}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Errorf("runner calls = %#v, want one cached squeue call %#v", runner.calls, wantCalls)
	}
}

func TestClientDetailsCacheAvoidsRepeatedCommands(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(`{"errors":[],"sinfo":[]}`), "squeue": []byte(`{"errors":[],"jobs":[]}`)}}
	client := newTestClient(t, runner)
	for range 2 {
		if _, err := client.Nodes(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Jobs(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls := len(runner.callsSnapshot()); calls != 2 {
		t.Errorf("runner calls = %d, want 2", calls)
	}
}

func TestClientDetailsRejectInvalidJSON(t *testing.T) {
	tests := []struct{ name, method, output string }{
		{name: "malformed", method: "nodes", output: `{`},
		{name: "reported errors", method: "nodes", output: `{"errors":[{"error":"denied"}],"sinfo":[]}`},
		{name: "node missing name", method: "nodes", output: `{"errors":[],"sinfo":[{"nodes":{"nodes":[]},"partition":{"name":"GPU"},"node":{"state":["IDLE"]},"cpus":{"allocated":0,"total":128},"memory":{"maximum":1},"gres":{"total":""}}]}`},
		{name: "node CPU invalid", method: "nodes", output: `{"errors":[],"sinfo":[{"nodes":{"nodes":["n1"]},"partition":{"name":"GPU"},"node":{"state":["IDLE"]},"cpus":{"allocated":129,"total":128},"memory":{"maximum":1},"gres":{"total":""}}]}`},
		{name: "job missing ID", method: "jobs", output: `{"errors":[],"jobs":[{"job_id":0,"user_name":"u","job_state":["RUNNING"],"node_count":{"set":true,"number":1}}]}`},
		{name: "oversized job name", method: "jobs", output: `{"errors":[],"jobs":[{"job_id":1,"name":"` + strings.Repeat("x", 2048) + `","user_name":"u","job_state":["RUNNING"],"node_count":{"set":true,"number":1}}]}`},
		{name: "negative user ID", method: "jobs", output: `{"errors":[],"jobs":[{"job_id":1,"user_name":"u","user_id":-1,"job_state":["RUNNING"],"node_count":1}]}`},
		{name: "zero explicitly set user ID", method: "jobs", output: `{"errors":[],"jobs":[{"job_id":1,"user_name":"u","user_id":{"set":true,"number":0},"job_state":["RUNNING"],"node_count":1}]}`},
		{name: "negative CPU count", method: "jobs", output: `{"errors":[],"jobs":[{"job_id":1,"user_name":"u","cpus":{"set":true,"number":-1},"job_state":["RUNNING"],"node_count":1}]}`},
		{name: "oversized working directory", method: "jobs", output: `{"errors":[],"jobs":[{"job_id":1,"user_name":"u","job_state":["RUNNING"],"node_count":1,"current_working_directory":"/` + strings.Repeat("x", 2048) + `"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{outputs: map[string][]byte{"sinfo": []byte(test.output), "squeue": []byte(test.output)}}
			client := newTestClient(t, runner)
			var err error
			if test.method == "nodes" {
				_, err = client.Nodes(context.Background())
			} else {
				_, err = client.Jobs(context.Background())
			}
			if err == nil {
				t.Fatal("details error = nil, want parse error")
			}
		})
	}
}

func TestSlurmNumberAcceptsScalarAndWrappedIntegers(t *testing.T) {
	for _, test := range []struct {
		json string
		want slurmNumber
	}{
		{json: `16`, want: slurmNumber{Set: true, Number: 16}},
		{json: `{"set":true,"infinite":false,"number":16}`, want: slurmNumber{Set: true, Number: 16}},
		{json: `{"set":false,"infinite":true,"number":0}`, want: slurmNumber{Infinite: true}},
	} {
		var got slurmNumber
		if err := json.Unmarshal([]byte(test.json), &got); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v", test.json, err)
		}
		if got != test.want {
			t.Errorf("Unmarshal(%s) = %#v, want %#v", test.json, got, test.want)
		}
	}
}

func TestJobsIgnoreUnsetWrappedUserID(t *testing.T) {
	jobs, err := parseJobsJSON([]byte(`{"errors":[],"jobs":[{"job_id":1,"user_name":"u","user_id":{"set":false,"number":10001},"job_state":["RUNNING"],"node_count":1}]}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if jobs[0].UserID != 0 {
		t.Errorf("UserID = %d, want 0 for unset wrapper", jobs[0].UserID)
	}
}

func TestClientDetailsWrapCommandErrors(t *testing.T) {
	sentinel := errors.New("scheduler unavailable")
	runner := &scriptedRunner{run: func(context.Context, string, ...string) ([]byte, error) { return nil, sentinel }}
	client := newTestClient(t, runner)
	if _, err := client.Nodes(context.Background()); !errors.Is(err, sentinel) {
		t.Errorf("Nodes() error = %v", err)
	}
	if _, err := client.Jobs(context.Background()); !errors.Is(err, sentinel) {
		t.Errorf("Jobs() error = %v", err)
	}
}
