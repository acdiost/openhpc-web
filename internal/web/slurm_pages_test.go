package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

func TestSlurmNodesPageShowsLiveEscapedData(t *testing.T) {
	provider := &stubNodeProvider{nodes: []cluster.Node{{Name: "node<31", Partition: "GPU", State: "mixed", AllocatedCPUs: 48, TotalCPUs: 128, MemoryMB: 510000, GRES: "gpu:rtx:8", Online: true}}}
	handler := newSlurmPageHandler(t, provider, nil)
	response := getAuthenticated(t, handler, "/slurm/nodes", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"节点与分区", "node&lt;31", "GPU", "mixed", "48 / 128", "510000 MB", "gpu:rtx:8"} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "node<31")
	assertBodyContains(t, response, `class="app-shell"`)
	assertBodyContains(t, response, `class="sidebar"`)
	assertBodyContains(t, response, `class="topbar"`)
}

func TestSlurmJobsPageShowsLiveEscapedData(t *testing.T) {
	provider := &stubJobProvider{jobs: []cluster.Job{{ID: "32940", Name: "<script>alert(1)</script>", User: "liyuxiang", Account: "jfzx", State: "RUNNING", Elapsed: "45:53", TimeLimit: "UNLIMITED", NodeCount: 1, NodesOrReason: "node31"}}}
	handler := newSlurmPageHandler(t, nil, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"Jobs", "32940", "&lt;script&gt;alert(1)&lt;/script&gt;", "liyuxiang", "RUNNING", "node31"} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "<script>alert(1)</script>")
	assertBodyContains(t, response, `class="app-shell"`)
}

func TestSlurmPagesUseLightApplicationTheme(t *testing.T) {
	handler := newSlurmPageHandler(t, &stubNodeProvider{}, &stubJobProvider{})
	request := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyNotContains(t, response, ".module-page{background:#111a21")
	assertBodyNotContains(t, response, "background:#17232c")
}

func TestSlurmPagesDegradeWithoutLeakingErrors(t *testing.T) {
	secret := errors.New("exec /secret/slurm: credential material")
	tests := []struct {
		path  string
		nodes cluster.NodeProvider
		jobs  cluster.JobProvider
	}{
		{path: "/slurm/nodes", nodes: &stubNodeProvider{err: secret}},
		{path: "/slurm/jobs", jobs: &stubJobProvider{err: secret}},
		{path: "/slurm/nodes"},
		{path: "/slurm/jobs"},
	}
	for _, test := range tests {
		handler := newSlurmPageHandler(t, test.nodes, test.jobs)
		response := getAuthenticated(t, handler, test.path, "zh")
		assertStatus(t, response, http.StatusOK)
		assertBodyContains(t, response, "Slurm 数据暂不可用")
		assertBodyNotContains(t, response, "/secret/slurm")
		assertBodyNotContains(t, response, "credential material")
	}
}

func TestSlurmPagesRequireAuthentication(t *testing.T) {
	handler := newSlurmPageHandler(t, &stubNodeProvider{}, &stubJobProvider{})
	for _, path := range []string{"/slurm/nodes", "/slurm/jobs"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, http.StatusFound)
	}
}

func newSlurmPageHandler(t *testing.T, nodes cluster.NodeProvider, jobs cluster.JobProvider) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, NodeProvider: nodes, JobProvider: jobs})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

func getAuthenticated(t *testing.T, handler http.Handler, path, lang string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(login(t, handler))
	if lang != "" {
		request.AddCookie(&http.Cookie{Name: "openhpc_language", Value: lang})
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type stubNodeProvider struct {
	nodes []cluster.Node
	err   error
}

func (p *stubNodeProvider) Nodes(context.Context) ([]cluster.Node, error) {
	return append([]cluster.Node(nil), p.nodes...), p.err
}

type stubJobProvider struct {
	jobs []cluster.Job
	err  error
}

func (p *stubJobProvider) Jobs(context.Context) ([]cluster.Job, error) {
	return append([]cluster.Job(nil), p.jobs...), p.err
}
