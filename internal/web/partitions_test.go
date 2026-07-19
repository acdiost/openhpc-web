package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/platform"
)

func TestNodesPageShowsOnlyNodeManagementData(t *testing.T) {
	partitions := &stubPartitionProvider{partitions: []cluster.Partition{{Name: "GPU<main", NodeCount: 3, OnlineNodes: 2, AllocatedCPUs: 64, TotalCPUs: 384, MemoryMB: 1530000, CPUUtilization: 17}}}
	nodes := &partitionNodeProvider{nodes: []cluster.Node{{Name: "node31", Partition: "GPU<main", State: "mixed", TotalCPUs: 128, Online: true}}}
	handler := newSlurmPageHandlerWithPartitions(t, nodes, partitions)
	response := getPartitionAuthenticated(t, handler, "/slurm/nodes", "zh")

	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"节点管理", "节点状态", "节点总数", "在线节点", "不可用节点", "node31", `id="nodes"`} {
		assertBodyContains(t, response, value)
	}
	for _, value := range []string{"分区状态", "2 / 3", "64 / 384", "17%", "1530000 MB", `id="partitions"`} {
		assertBodyNotContains(t, response, value)
	}
	assertBodyNotContains(t, response, "GPU<main")
	if partitions.calls != 0 {
		t.Errorf("Partitions() calls = %d, want 0", partitions.calls)
	}
}

func TestNewNodeSummaryCountsAvailability(t *testing.T) {
	summary := newNodeSummary([]cluster.Node{
		{Name: "node31", Online: true},
		{Name: "node32", Online: false},
		{Name: "node33", Online: true},
	})
	if summary != (nodeSummaryView{Total: 3, Online: 2, Offline: 1}) {
		t.Fatalf("node summary = %#v", summary)
	}
	if empty := newNodeSummary(nil); empty != (nodeSummaryView{}) {
		t.Fatalf("empty node summary = %#v", empty)
	}
}

func TestNodesPageShowsAvailableNodeAction(t *testing.T) {
	handler := newSlurmPageHandlerWithNodeAdmin(t, &partitionNodeProvider{nodes: []cluster.Node{
		{Name: "node31", Partition: "GPU", State: "idle", Online: true},
		{Name: "node32", Partition: "GPU", State: "down", Online: false},
	}}, nil, &stubNodeAdmin{})

	response := getPartitionAuthenticated(t, handler, "/slurm/nodes", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{`action="/slurm/nodes/state"`, `name="name" value="node31"`, `data-node-state-trigger="down"`, `data-node-state-trigger="drain"`, `data-node-state-value`, `value="resume"`, `class="node-offline-button"`, `class="node-drain-button"`, `class="node-resume-button"`, `aria-controls="node-state-modal"`, `aria-label="下线Down: node31"`, "下线Down", "维护Drain", "恢复Resume", `<dialog id="node-state-modal"`, `data-node-state-reason`, `<noscript>`, `class="node-state-fallback"`, `name="reason"`, `required`} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, `class="node-state-form"`)
}

func TestNodesPageChangesNodeState(t *testing.T) {
	admin := &stubNodeAdmin{}
	handler := newSlurmPageHandlerWithNodeAdmin(t, &partitionNodeProvider{nodes: []cluster.Node{{Name: "node31", Online: true}}}, nil, admin)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/slurm/nodes/state", url.Values{"name": {"node31"}, "state": {"down"}, "reason": {"scheduled maintenance"}}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/nodes?saved=down#nodes")
	if !reflect.DeepEqual(admin.calls, []nodeAdminCall{{name: "node31", state: cluster.NodeStateDown, reason: "scheduled maintenance"}}) {
		t.Fatalf("node admin calls = %#v", admin.calls)
	}

	response = postProtectedForm(handler, "/slurm/nodes/state", url.Values{"name": {"node31"}, "state": {"drain"}, "reason": {"hardware inspection"}}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/nodes?saved=drain#nodes")

	response = postProtectedForm(handler, "/slurm/nodes/state", url.Values{"name": {"node31"}, "state": {"resume"}, "reason": {"stale browser value"}}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/nodes?saved=resume#nodes")
	if !reflect.DeepEqual(admin.calls, []nodeAdminCall{
		{name: "node31", state: cluster.NodeStateDown, reason: "scheduled maintenance"},
		{name: "node31", state: cluster.NodeStateDrain, reason: "hardware inspection"},
		{name: "node31", state: cluster.NodeStateResume},
	}) {
		t.Fatalf("node admin calls = %#v", admin.calls)
	}
}

func TestNodeStateAuditActionIncludesState(t *testing.T) {
	if action := nodeStateAuditAction(cluster.NodeStateDrain); action != "slurm.node.state.drain" {
		t.Fatalf("nodeStateAuditAction() = %q", action)
	}
}

func TestNodesPageRejectsUnknownNodeAndReportsUpdateFailure(t *testing.T) {
	provider := &partitionNodeProvider{nodes: []cluster.Node{{Name: "node31", Online: true}}}
	admin := &stubNodeAdmin{err: errors.New("scontrol failed")}
	handler := newSlurmPageHandlerWithNodeAdmin(t, provider, nil, admin)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/slurm/nodes/state", url.Values{"name": {"not-a-node"}, "state": {"down"}, "reason": {"maintenance"}}, session, csrf)
	assertStatus(t, response, http.StatusBadRequest)
	if len(admin.calls) != 0 {
		t.Fatalf("node admin calls = %#v, want none", admin.calls)
	}

	response = postProtectedForm(handler, "/slurm/nodes/state", url.Values{"name": {"node31"}, "state": {"down"}, "reason": {"maintenance"}}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/nodes?error=update_failed#nodes")
}

func TestNodesPageRequiresReasonForDownAndDrain(t *testing.T) {
	admin := &stubNodeAdmin{}
	handler := newSlurmPageHandlerWithNodeAdmin(t, &partitionNodeProvider{nodes: []cluster.Node{{Name: "node31", Online: true}}}, nil, admin)
	session, csrf := loginWithCSRF(t, handler)

	for _, state := range []string{"down", "drain"} {
		response := postProtectedForm(handler, "/slurm/nodes/state", url.Values{"name": {"node31"}, "state": {state}}, session, csrf)
		assertStatus(t, response, http.StatusSeeOther)
		assertHeader(t, response, "Location", "/slurm/nodes?error=reason_required#nodes")
	}
	if len(admin.calls) != 0 {
		t.Fatalf("node admin calls = %#v, want none", admin.calls)
	}
}

func TestNodesPageHidesNodeProviderFailureDetails(t *testing.T) {
	secret := errors.New("exec /secret/sinfo: credential material")
	handler := newSlurmPageHandlerWithPartitions(t, &partitionNodeProvider{err: secret}, nil)
	response := getPartitionAuthenticated(t, handler, "/slurm/nodes", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "Slurm data is temporarily unavailable")
	assertBodyNotContains(t, response, "/secret/sinfo")
	assertBodyNotContains(t, response, "credential material")
}

func TestPartitionManagementPageRendersLiveNodesAndStoredPartitions(t *testing.T) {
	handler := newPartitionManagementHandler(t, filepath.Join(t.TempDir(), "state.db"), &partitionNodeProvider{
		nodes: []cluster.Node{{Name: "node31", Partition: "GPU", State: "idle", TotalCPUs: 128, Online: true}, {Name: "node32", Partition: "GPU", State: "idle", TotalCPUs: 128, Online: true}},
	}, nil)

	response := getPartitionAuthenticated(t, handler, "/slurm/partitions", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"分区管理", "创建分区", "系统分区", "平台分区", "GPU", "node31", "node32", `name="name"`, `name="nodes"`, `value="node31"`, `value="node32"`} {
		assertBodyContains(t, response, value)
	}
}

func TestPartitionManagementPageUsesModalsForPartitionOperations(t *testing.T) {
	handler := newPartitionManagementHandler(t, filepath.Join(t.TempDir(), "state.db"), &partitionNodeProvider{
		nodes: []cluster.Node{{Name: "node31", Partition: "GPU", State: "idle", Online: true}},
	}, nil)
	session, csrf := loginWithCSRF(t, handler)
	response := postProtectedForm(handler, "/slurm/partitions", url.Values{
		"name":  {"test"},
		"nodes": {"node31"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)

	response = getPartitionAuthenticated(t, handler, "/slurm/partitions", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		`id="partition-editor-modal"`, `id="partition-delete-modal"`,
		`data-partition-create`, `data-partition-edit`, `data-partition-delete`,
		`aria-haspopup="dialog"`, `data-partition-modal-close`,
		`action="/slurm/partitions" method="get"`, `action="/slurm/partitions/delete"`, `href="/slurm/partitions" class="modal-close"`,
	} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, `id="editor"`)

	response = getPartitionAuthenticated(t, handler, "/slurm/partitions?modal=create", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, `open data-partition-open`)

	response = getPartitionAuthenticated(t, handler, "/slurm/partitions?name=test", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, `value="test" autocomplete="off" spellcheck="false" required data-partition-name-input readonly`)

	response = getPartitionAuthenticated(t, handler, "/slurm/partitions?saved=created&name=test", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyNotContains(t, response, `data-partition-open`)
}

func TestPartitionManagementPageCreatesPatchesAndDeletesStoredPartition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	handler := newPartitionManagementHandler(t, path, &partitionNodeProvider{
		nodes: []cluster.Node{{Name: "node31", Partition: "GPU", State: "idle", TotalCPUs: 128, Online: true}, {Name: "node32", Partition: "GPU", State: "idle", TotalCPUs: 128, Online: true}},
	}, nil)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/slurm/partitions", url.Values{
		"name":  {"test"},
		"nodes": {"node31", "node32"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/partitions?saved=created&name=test")

	response = getPartitionAuthenticated(t, handler, "/slurm/partitions", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"Partition management", "GPU", "test", "node31, node32", "Patched", "Edit partition", "Delete partition"} {
		assertBodyContains(t, response, value)
	}
	store, err := platform.OpenPartitionStore(path)
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	spec, found, err := store.Get(context.Background(), "test")
	if err != nil || !found || len(spec.Nodes) != 2 {
		t.Fatalf("stored partition after create = %#v, %v, %v", spec, found, err)
	}
	_ = store.Close()

	response = postProtectedForm(handler, "/slurm/partitions", url.Values{
		"name":  {"test"},
		"nodes": {"node31"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/partitions?saved=updated&name=test")

	response = getPartitionAuthenticated(t, handler, "/slurm/partitions", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "node31")
	assertBodyContains(t, response, "Patched")
	store, err = platform.OpenPartitionStore(path)
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	defer store.Close()
	spec, found, err = store.Get(context.Background(), "test")
	if err != nil || !found || len(spec.Nodes) != 1 || spec.Nodes[0] != "node31" {
		t.Fatalf("stored partition after patch = %#v, %v, %v", spec, found, err)
	}

	response = postProtectedForm(handler, "/slurm/partitions/delete", url.Values{
		"name": {"test"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/partitions?saved=deleted")

	store, err = platform.OpenPartitionStore(path)
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	defer store.Close()
	if _, found, err := store.Get(context.Background(), "test"); err != nil || found {
		t.Fatalf("stored partition after delete = %v, %v", found, err)
	}
}

func TestPartitionManagementPageSyncsPartitionToSlurm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer := &stubPartitionAdmin{}
	handler := newPartitionManagementHandlerWithAdmin(t, path, &partitionNodeProvider{
		nodes: []cluster.Node{{Name: "node31", Partition: "GPU", State: "idle", TotalCPUs: 128, Online: true}},
	}, nil, writer)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/slurm/partitions", url.Values{
		"name":  {"test"},
		"nodes": {"node31"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	if !reflect.DeepEqual(writer.calls, []partitionAdminCall{{name: "test", nodes: []string{"node31"}}}) {
		t.Fatalf("partition admin calls = %#v", writer.calls)
	}
}

func TestPartitionManagementPageImportsSystemPartitionsOnStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	handler := newPartitionManagementHandler(t, path, &partitionNodeProvider{
		nodes: []cluster.Node{{Name: "node31", Partition: "GPU", State: "idle", TotalCPUs: 128, Online: true}},
	}, nil)

	response := getPartitionAuthenticated(t, handler, "/slurm/partitions", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "System partitions")
	assertBodyContains(t, response, "GPU")
	assertBodyContains(t, response, "Read-only")
	store, err := platform.OpenPartitionStore(path)
	if err != nil {
		t.Fatalf("OpenPartitionStore() error = %v", err)
	}
	defer store.Close()
	spec, found, err := store.Get(context.Background(), "GPU")
	if err != nil || !found || spec.Managed {
		t.Fatalf("system partition after startup import = %#v, %v, %v", spec, found, err)
	}
}

func TestPartitionManagementPageRejectsSystemPartitionEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	handler := newPartitionManagementHandler(t, path, &partitionNodeProvider{
		nodes: []cluster.Node{{Name: "node31", Partition: "GPU", State: "idle", TotalCPUs: 128, Online: true}},
	}, nil)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/slurm/partitions", url.Values{
		"name":  {"GPU"},
		"nodes": {"node31"},
	}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/slurm/partitions?error=%E5%88%86%E5%8C%BA+GPU+%E4%B8%BA%E5%8F%AA%E8%AF%BB%E3%80%82&name=GPU")
}

func TestPartitionManagementPageRequiresAuthentication(t *testing.T) {
	handler := newPartitionManagementHandler(t, filepath.Join(t.TempDir(), "state.db"), &partitionNodeProvider{}, nil)
	request := httptest.NewRequest(http.MethodGet, "/slurm/partitions", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/login?next=%2Fslurm%2Fpartitions")
}

func newPartitionManagementHandler(t *testing.T, databasePath string, nodes cluster.NodeProvider, partitions cluster.PartitionProvider) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, DatabasePath: databasePath, NodeProvider: nodes, PartitionProvider: partitions})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

func newPartitionManagementHandlerWithAdmin(t *testing.T, databasePath string, nodes cluster.NodeProvider, partitions cluster.PartitionProvider, admin cluster.PartitionAdmin) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, DatabasePath: databasePath, NodeProvider: nodes, PartitionProvider: partitions, PartitionAdmin: admin})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
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

func newSlurmPageHandlerWithNodeAdmin(t *testing.T, nodes cluster.NodeProvider, partitions cluster.PartitionProvider, admin cluster.NodeAdmin) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, NodeProvider: nodes, PartitionProvider: partitions, NodeAdmin: admin})
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

type partitionAdminCall struct {
	name  string
	nodes []string
}

type stubPartitionAdmin struct {
	calls []partitionAdminCall
	err   error
}

type nodeAdminCall struct {
	name   string
	state  cluster.NodeState
	reason string
}

type stubNodeAdmin struct {
	calls []nodeAdminCall
	err   error
}

func (s *stubNodeAdmin) SetNodeState(_ context.Context, name string, state cluster.NodeState, reason string) error {
	s.calls = append(s.calls, nodeAdminCall{name: name, state: state, reason: reason})
	return s.err
}

func (s *stubPartitionAdmin) ApplyPartition(_ context.Context, name string, nodes []string) error {
	s.calls = append(s.calls, partitionAdminCall{name: name, nodes: append([]string(nil), nodes...)})
	return s.err
}

func (s *stubPartitionAdmin) DeletePartition(_ context.Context, name string) error {
	s.calls = append(s.calls, partitionAdminCall{name: name, nodes: nil})
	return s.err
}
