package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func TestNodesPageEmbedsPartitionAndNodeSnapshots(t *testing.T) {
	partitions := &stubPartitionProvider{partitions: []cluster.Partition{{Name: "GPU<main", NodeCount: 3, OnlineNodes: 2, AllocatedCPUs: 64, TotalCPUs: 384, MemoryMB: 1530000, CPUUtilization: 17}}}
	nodes := &partitionNodeProvider{nodes: []cluster.Node{{Name: "node31", Partition: "GPU<main", State: "mixed", TotalCPUs: 128, Online: true}}}
	handler := newSlurmPageHandlerWithPartitions(t, nodes, partitions)
	response := getPartitionAuthenticated(t, handler, "/slurm/nodes", "zh")

	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"分区状态", "节点状态", "GPU&lt;main", "2 / 3", "64 / 384", "17%", "1530000 MB", "node31", `id="partitions"`, `id="nodes"`} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "GPU<main")
	if partitions.calls != 1 {
		t.Errorf("Partitions() calls = %d, want 1", partitions.calls)
	}
}

func TestNodesPageSeparatesPartitionAndNodeFailures(t *testing.T) {
	secret := errors.New("exec /secret/sinfo: credential material")
	tests := []struct {
		name       string
		nodes      *partitionNodeProvider
		partitions *stubPartitionProvider
		want       string
	}{
		{
			name: "partitions unavailable",
			nodes: &partitionNodeProvider{nodes: []cluster.Node{{
				Name: "node31", Partition: "GPU", State: "idle", TotalCPUs: 1, Online: true,
			}}},
			partitions: &stubPartitionProvider{err: secret}, want: "node31",
		},
		{
			name: "nodes unavailable", nodes: &partitionNodeProvider{err: secret},
			partitions: &stubPartitionProvider{partitions: []cluster.Partition{{Name: "GPU", NodeCount: 1}}},
			want:       "GPU",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newSlurmPageHandlerWithPartitions(t, test.nodes, test.partitions)
			response := getPartitionAuthenticated(t, handler, "/slurm/nodes", "en")
			assertStatus(t, response, http.StatusOK)
			assertBodyContains(t, response, "Slurm data is temporarily unavailable")
			assertBodyContains(t, response, test.want)
			assertBodyNotContains(t, response, "/secret/sinfo")
			assertBodyNotContains(t, response, "credential material")
		})
	}
}

func TestLegacyPartitionsRouteRedirectsInsideNodesPage(t *testing.T) {
	handler := newSlurmPageHandlerWithPartitions(t, &partitionNodeProvider{}, &stubPartitionProvider{})
	response := getPartitionAuthenticated(t, handler, "/slurm/partitions", "en")
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/slurm/nodes#partitions")
}

func TestLegacyPartitionsRouteRequiresAuthentication(t *testing.T) {
	provider := &stubPartitionProvider{}
	handler := newSlurmPageHandlerWithPartitions(t, &partitionNodeProvider{}, provider)
	request := httptest.NewRequest(http.MethodGet, "/slurm/partitions", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/login?next=%2Fslurm%2Fpartitions")
	if provider.calls != 0 {
		t.Errorf("Partitions() calls = %d, want 0", provider.calls)
	}
}

func newSlurmPageHandlerWithPartitions(t *testing.T, nodes cluster.NodeProvider, partitions cluster.PartitionProvider) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, NodeProvider: nodes, PartitionProvider: partitions})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

func getPartitionAuthenticated(t *testing.T, handler http.Handler, path, lang string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(login(t, handler))
	request.AddCookie(&http.Cookie{Name: "openhpc_language", Value: lang})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type partitionNodeProvider struct {
	nodes []cluster.Node
	err   error
}

func (p *partitionNodeProvider) Nodes(context.Context) ([]cluster.Node, error) {
	return append([]cluster.Node(nil), p.nodes...), p.err
}

type stubPartitionProvider struct {
	partitions []cluster.Partition
	err        error
	calls      int
}

func (p *stubPartitionProvider) Partitions(context.Context) ([]cluster.Partition, error) {
	p.calls++
	return append([]cluster.Partition(nil), p.partitions...), p.err
}
