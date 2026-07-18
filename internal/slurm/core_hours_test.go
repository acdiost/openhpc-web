package slurm

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

func TestClientCoreHoursUsesJSONAllocationsAndAggregatesWindowOverlap(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	runner := &scriptedRunner{outputs: map[string][]byte{"sacct": []byte(`{
		"errors":[],"jobs":[
			{"account":"a","user":"alice","partition":"cpu","time":{"start":1784340000,"end":1784343600},"state":{"current":["COMPLETED"]},"tres":{"allocated":[{"type":"cpu","count":4}]}},
			{"account":"a","user":"bob","partition":"gpu","time":{"start":1784345400,"end":0},"state":{"current":["RUNNING"]},"tres":{"allocated":[{"type":"cpu","count":2}]}},
			{"account":"a","user":"pending","time":{"start":0,"end":0},"state":{"current":["PENDING"]},"tres":{"allocated":[{"type":"cpu","count":64}]}},
			{"account":"b","user":"carol","time":{"start":1784250000,"end":1784264400},"state":{"current":["CANCELLED"]},"tres":{"allocated":[{"type":"cpu","count":1}]}}
		]}`)}}
	client := newTestClient(t, runner)
	client.now = func() time.Time { return now }

	summary, err := client.CoreHours(context.Background(), cluster.CoreHourPeriod24Hours)
	if err != nil {
		t.Fatalf("CoreHours() error = %v", err)
	}
	if summary.CoreSeconds != 21_600 || summary.AllocationCount != 3 {
		t.Fatalf("summary = %#v", summary)
	}
	wantAccounts := []cluster.CoreHourGroup{{Name: "a", CoreSeconds: 18_000, AllocationCount: 2}, {Name: "b", CoreSeconds: 3_600, AllocationCount: 1}}
	if !reflect.DeepEqual(summary.Accounts, wantAccounts) {
		t.Errorf("accounts = %#v, want %#v", summary.Accounts, wantAccounts)
	}
	wantCall := commandCall{path: filepath.Join("/opt/slurm/bin", "sacct"), args: []string{
		"--json", "--allocations", "--allusers", "--starttime=2026-07-17T12:00:00", "--endtime=2026-07-18T12:00:00",
	}}
	if calls := runner.callsSnapshot(); !reflect.DeepEqual(calls, []commandCall{wantCall}) {
		t.Errorf("calls = %#v", calls)
	}
}

func TestClientCoreHoursRejectsUnknownPeriodBeforeRunningCommand(t *testing.T) {
	runner := &scriptedRunner{}
	client := newTestClient(t, runner)
	if _, err := client.CoreHours(context.Background(), cluster.CoreHourPeriod("custom")); err == nil {
		t.Fatal("CoreHours() error = nil, want invalid period error")
	}
	if calls := runner.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("calls = %#v, want none", calls)
	}
}

func TestParseCoreHoursRejectsOverflow(t *testing.T) {
	output := []byte(`{"errors":[],"jobs":[{"account":"a","user":"u","time":{"start":1,"end":9223372036854775807},"state":{"current":["COMPLETED"]},"tres":{"allocated":[{"type":"cpu","count":9223372036854775807}]}}]}`)
	_, err := parseCoreHoursJSON(output, time.Unix(1, 0), time.Unix(9223372036854775807, 0))
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("parseCoreHoursJSON() error = %v, want overflow", err)
	}
}
