package slurm

import (
	"context"
	"path/filepath"
	"reflect"
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
			return []byte(`{"errors":[],"accounts":[{"name":"jfzx","description":"Research","organization":"lab","coordinators":[{}],"associations":[{},{}]}]}`), nil
		case "user":
			return []byte(`{"errors":[],"users":[{"name":"alice","administrator_level":["None"],"default":{"account":"jfzx","wckey":""},"associations":[{}]}]}`), nil
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
		Accounts: []cluster.Account{{Name: "jfzx", Description: "Research", Organization: "lab", CoordinatorCount: 1, AssociationCount: 2}},
		Users:    []cluster.SlurmUser{{Name: "alice", AdministratorLevel: "None", DefaultAccount: "jfzx", AssociationCount: 1}},
	}
	if !reflect.DeepEqual(directory, wantDirectory) {
		t.Errorf("directory = %#v", directory)
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
		_, _ = client.QoS(context.Background())
	}
	if calls := len(runner.callsSnapshot()); calls != 3 {
		t.Errorf("calls = %d", calls)
	}
}

func TestClientAccountingRejectsReportedErrors(t *testing.T) {
	runner := &scriptedRunner{outputs: map[string][]byte{"sacctmgr": []byte(`{"errors":[{"error":"denied"}]}`)}}
	client := newTestClient(t, runner)
	if _, err := client.AccountDirectory(context.Background()); err == nil {
		t.Fatal("AccountDirectory() error = nil")
	}
}
