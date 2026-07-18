package slurm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

func TestClientAccountingParsesSacctmgrJSON(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{
		"sacctmgr": nil,
	}}
	runner.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[2] {
		case "account":
			return []byte(`{"errors":[],"accounts":[{"name":"jfzx","description":"Research","organization":"lab","coordinators":[{}],"associations":[{"id":3,"cluster":"hpc","account":"jfzx","user":"bob","partition":"GPU"},{"id":1,"cluster":"hpc","account":"jfzx","user":"","partition":""},{"id":2,"cluster":"hpc","account":"jfzx","user":"alice","partition":""}]}]}`), nil
		case "user":
			return []byte(`{"errors":[],"users":[{"name":"alice","administrator_level":["None"],"default":{"account":"jfzx","wckey":""},"associations":[{"id":2,"cluster":"hpc","account":"jfzx","user":"alice","partition":""},{"id":4,"cluster":"hpc","account":"jfzx","user":"alice","partition":"GPU"}]}]}`), nil
		case "qos":
			return []byte(`{"errors":[],"qos":[{"name":"normal","description":"Default","priority":{"set":true,"number":10},"usage_factor":{"set":true,"number":1.5},"limits":{"max":{"jobs":{"count":{"infinite":true,"set":false,"number":0}}}}}]}`), nil
		}
		return nil, nil
	}
	client := newTestClient(t, runner)
	directory, err := client.AccountDirectory(context.Background())
	if err != nil {
		t.Fatalf("AccountDirectory() error = %v", err)
	}
	wantDirectory := cluster.AccountDirectory{
		Accounts: []cluster.Account{{Name: "jfzx", Description: "Research", Organization: "lab", CoordinatorCount: 1, AssociationCount: 3}},
		Users:    []cluster.SlurmUser{{Name: "alice", AdministratorLevel: "None", DefaultAccount: "jfzx", AssociationCount: 2}},
	}
	if !reflect.DeepEqual(directory, wantDirectory) {
		t.Errorf("directory = %#v", directory)
	}
	associations, err := client.Associations(context.Background())
	if err != nil {
		t.Fatalf("Associations() error = %v", err)
	}
	wantAssociations := []cluster.Association{
		{ID: 1, Cluster: "hpc", Account: "jfzx"},
		{ID: 2, Cluster: "hpc", Account: "jfzx", User: "alice"},
		{ID: 4, Cluster: "hpc", Account: "jfzx", User: "alice", Partition: "GPU"},
		{ID: 3, Cluster: "hpc", Account: "jfzx", User: "bob", Partition: "GPU"},
	}
	if !reflect.DeepEqual(associations, wantAssociations) {
		t.Errorf("associations = %#v", associations)
	}
	qos, err := client.QoS(context.Background())
	if err != nil {
		t.Fatalf("QoS() error = %v", err)
	}
	wantQoS := []cluster.QoS{{Name: "normal", Description: "Default", Priority: 10, UsageFactor: 1.5, MaxJobsUnlimited: true}}
	if !reflect.DeepEqual(qos, wantQoS) {
		t.Errorf("qos = %#v", qos)
	}
	wantCalls := []commandCall{
		{path: filepath.Join("/opt/slurm/bin", "sacctmgr"), args: []string{"--json", "show", "account", "WithAssoc"}},
		{path: filepath.Join("/opt/slurm/bin", "sacctmgr"), args: []string{"--json", "show", "user", "WithAssoc"}},
		{path: filepath.Join("/opt/slurm/bin", "sacctmgr"), args: []string{"--json", "show", "qos"}},
	}
	if calls := runner.callsSnapshot(); !reflect.DeepEqual(calls, wantCalls) {
		t.Errorf("calls = %#v", calls)
	}
}

func TestClientAccountingCachesSnapshots(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[2] == "account" {
			return []byte(`{"errors":[],"accounts":[]}`), nil
		}
		if args[2] == "user" {
			return []byte(`{"errors":[],"users":[]}`), nil
		}
		return []byte(`{"errors":[],"qos":[]}`), nil
	}}
	client := newTestClient(t, runner)
	for range 2 {
		_, _ = client.AccountDirectory(context.Background())
		_, _ = client.Associations(context.Background())
		_, _ = client.QoS(context.Background())
	}
	if calls := len(runner.callsSnapshot()); calls != 3 {
		t.Errorf("calls = %d", calls)
	}
}

func TestClientAccountingAssociationSnapshotIsDefensiveCopy(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[2] == "account" {
			return []byte(`{"errors":[],"accounts":[{"name":"research","associations":[{"id":7,"cluster":"hpc","account":"research","user":"alice","partition":""}]}]}`), nil
		}
		return []byte(`{"errors":[],"users":[]}`), nil
	}}
	client := newTestClient(t, runner)
	first, err := client.Associations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first[0].Account = "mutated"
	second, err := client.Associations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Account != "research" {
		t.Fatalf("cached association was mutated: %#v", second)
	}
}

func TestClientAccountingAssociationErrorsDoNotBreakDirectory(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[2] == "account" {
			return []byte(`{"errors":[],"accounts":[{"name":"research","associations":[{"id":0,"cluster":"hpc","account":"research"}]}]}`), nil
		}
		return []byte(`{"errors":[],"users":[]}`), nil
	}}
	client := newTestClient(t, runner)
	directory, err := client.AccountDirectory(context.Background())
	if err != nil || len(directory.Accounts) != 1 {
		t.Fatalf("AccountDirectory() = (%#v, %v)", directory, err)
	}
	if _, err := client.Associations(context.Background()); err == nil {
		t.Fatal("Associations() error = nil, want invalid association error")
	}
	if calls := len(runner.callsSnapshot()); calls != 2 {
		t.Fatalf("calls = %d, want shared two-command cache", calls)
	}
}

func TestClientAccountingRejectsConflictingAssociationIDs(t *testing.T) {
	runner := &scriptedRunner{run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[2] == "account" {
			return []byte(`{"errors":[],"accounts":[{"name":"research","associations":[{"id":9,"cluster":"hpc","account":"research","user":"alice","partition":""}]}]}`), nil
		}
		return []byte(`{"errors":[],"users":[{"name":"alice","associations":[{"id":9,"cluster":"hpc","account":"other","user":"alice","partition":""}]}]}`), nil
	}}
	client := newTestClient(t, runner)
	if _, err := client.Associations(context.Background()); err == nil {
		t.Fatal("Associations() error = nil, want conflicting ID error")
	}
}

func TestParseAssociationRecordsRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		record string
	}{
		{name: "missing ID", record: `{"cluster":"hpc","account":"research"}`},
		{name: "zero ID", record: `{"id":0,"cluster":"hpc","account":"research"}`},
		{name: "negative ID", record: `{"id":-1,"cluster":"hpc","account":"research"}`},
		{name: "missing cluster", record: `{"id":1,"account":"research"}`},
		{name: "missing account", record: `{"id":1,"cluster":"hpc"}`},
		{name: "control character", record: `{"id":1,"cluster":"hpc","account":"research","user":"alice\u0001"}`},
		{name: "oversized field", record: `{"id":1,"cluster":"hpc","account":"` + strings.Repeat("a", maxDetailFieldLength+1) + `"}`},
		{name: "malformed JSON", record: `{"id":`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseAssociationRecords([]json.RawMessage{json.RawMessage(test.record)}); err == nil {
				t.Fatal("parseAssociationRecords() error = nil")
			}
		})
	}
}

func TestParseAssociationRecordsRejectsTooManyRecords(t *testing.T) {
	records := strings.Repeat(`{"id":1},`, 10_000) + `{"id":1}`
	accounts := []byte(`{"errors":[],"accounts":[{"name":"research","associations":[` + records + `]}]}`)
	users := []byte(`{"errors":[],"users":[]}`)
	directory, err := parseAccountsJSON(accounts)
	if err != nil || len(directory) != 1 || directory[0].AssociationCount != 10_001 {
		t.Fatalf("parseAccountsJSON() = (%#v, %v), want available directory", directory, err)
	}
	if _, err := parseAssociationsJSON(accounts, users); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("parseAssociationsJSON() error = %v, want record limit error", err)
	}
}

func TestClientAccountingRejectsReportedErrors(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sacctmgr": []byte(`{"errors":[{"error":"denied"}]}`)}}
	client := newTestClient(t, runner)
	if _, err := client.AccountDirectory(context.Background()); err == nil {
		t.Fatal("AccountDirectory() error = nil")
	}
}
