package slurm

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

func TestClientJobResourceUsageParsesSstat(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sstat": []byte(
		"32943.batch|00:10:15|1024K|2048K|4M|8M|cpu=02:44:00,energy=0\n" +
			"32943.extern|01:02|512K|1.5M|2M|3M|cpu=00:02:00,energy=0\n",
	)}}
	client := newTestClient(t, runner)
	client.now = func() time.Time { return time.Unix(1_721_286_400, 0).UTC() }

	usage, err := client.JobResourceUsage(context.Background(), 32943)
	if err != nil {
		t.Fatalf("JobResourceUsage() error = %v", err)
	}
	want := cluster.JobResourceUsage{
		JobID: "32943", SampledAt: "2024-07-18T07:06:40Z", TotalCPUSeconds: 9960, MaxRSSBytes: 2 << 20,
		Steps: []cluster.JobResourceStep{
			{Step: "batch", AveCPU: "00:10:15", AveCPUSeconds: 615, TotalCPU: "02:44:00", TotalCPUSeconds: 9840, AveRSS: "1024K", AveRSSBytes: 1 << 20, MaxRSS: "2048K", MaxRSSBytes: 2 << 20, AveVMSize: "4M", AveVMSizeBytes: 4 << 20, MaxVMSize: "8M", MaxVMSizeBytes: 8 << 20},
			{Step: "extern", AveCPU: "01:02", AveCPUSeconds: 62, TotalCPU: "00:02:00", TotalCPUSeconds: 120, AveRSS: "512K", AveRSSBytes: 512 << 10, MaxRSS: "1.5M", MaxRSSBytes: 1572864, AveVMSize: "2M", AveVMSizeBytes: 2 << 20, MaxVMSize: "3M", MaxVMSizeBytes: 3 << 20},
		},
	}
	if !reflect.DeepEqual(usage, want) {
		t.Errorf("JobResourceUsage() = %#v, want %#v", usage, want)
	}
	wantCalls := []commandCall{{path: filepath.Join("/opt/slurm/bin", "sstat"), args: []string{
		"--jobs=32943", "--allsteps", "--noheader", "--parsable2", "--format=JobID,AveCPU,AveRSS,MaxRSS,AveVMSize,MaxVMSize,TRESUsageInTot",
	}}}
	if calls := runner.callsSnapshot(); !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("runner calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestParseSstatRejectsMalformedAndMismatchedRows(t *testing.T) {
	for _, output := range []string{
		"32943.batch|00:01|1M\n",
		"32944.batch|00:01|1M|1M|1M|1M|cpu=00:02\n",
		"32943.batch|not-time|1M|1M|1M|1M|cpu=00:02\n",
		"32943.batch|00:01|secret|1M|1M|1M|cpu=00:02\n",
		"32943.batch|00:01|1M|1M|1M|1M|energy=0\n",
	} {
		if _, err := parseSstat([]byte(output), 32943, time.Now()); err == nil {
			t.Errorf("parseSstat(%q) error = nil", output)
		}
	}
}

func TestParseSstatKeepsStepsWithUnavailableAccountingFields(t *testing.T) {
	usage, err := parseSstat([]byte(
		"32943.batch||Unknown||0|N/A|cpu=N/A,energy=0\r\n"+
			"32943.extern||N/A|Unknown||0|\r\n",
	), 32943, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage.Steps) != 2 || usage.TotalCPUSeconds != 0 || usage.MaxRSSBytes != 0 {
		t.Errorf("usage = %#v", usage)
	}
}

func TestParseSstatAcceptsObservedSlurmOutput(t *testing.T) {
	output := []byte("32810.batch|38-21:38:19|25198776K|25198972K|0|0|cpu=38-21:38:19,energy=0,fs/disk=7626592458,mem=25198776K,pages=0,vmem=0\n")
	usage, err := parseSstat(output, 32810, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if usage.TotalCPUSeconds != 3_361_099 || usage.MaxRSSBytes != 25_198_972<<10 {
		t.Errorf("usage = %#v", usage)
	}
	if len(usage.Steps) != 1 || usage.Steps[0].TotalCPU != "38-21:38:19" {
		t.Errorf("steps = %#v", usage.Steps)
	}
}

func TestParseSstatAllowsNoActiveSteps(t *testing.T) {
	usage, err := parseSstat(nil, 32943, time.Unix(1_721_286_400, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if usage.JobID != "32943" || len(usage.Steps) != 0 {
		t.Errorf("usage = %#v", usage)
	}
}
