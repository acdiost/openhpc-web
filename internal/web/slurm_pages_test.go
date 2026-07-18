package web

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
	assertBodyContains(t, response, `href="/slurm/jobs/32940"`)
}

func TestSlurmJobDetailShowsEscapedData(t *testing.T) {
	provider := &stubJobProvider{jobs: []cluster.Job{{
		ID: "32940", Name: "<script>alert(1)</script>", User: "user<admin", Account: "a&b",
		State: "RUNNING", Elapsed: "45:53", TimeLimit: "UNLIMITED", NodeCount: 2, NodesOrReason: "node<31",
	}}}
	handler := newSlurmPageHandler(t, nil, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs/32940", "en")

	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		"Job details", "Back to jobs", "32940", "&lt;script&gt;alert(1)&lt;/script&gt;",
		"user&lt;admin", "a&amp;b", "RUNNING", "45:53", "UNLIMITED", "node&lt;31", "Node count", "2",
		`href="/slurm/jobs"`, `aria-current="page"`, `data-component="app-shell"`,
	} {
		assertBodyContains(t, response, value)
	}
	for _, unsafe := range []string{"<script>alert(1)</script>", "user<admin", "node<31"} {
		assertBodyNotContains(t, response, unsafe)
	}
	if provider.jobCalls != 1 || provider.lastJobID != 32940 {
		t.Errorf("Job() calls/id = (%d, %d), want (1, 32940)", provider.jobCalls, provider.lastJobID)
	}
}

func TestSlurmJobDetailRejectsInvalidIDsBeforeProviderLookup(t *testing.T) {
	for _, jobID := range []string{"0", "-1", "abc", "1.2", "9223372036854775808", "0001"} {
		t.Run(jobID, func(t *testing.T) {
			provider := &stubJobProvider{}
			handler := newSlurmPageHandler(t, nil, provider)
			response := getAuthenticated(t, handler, "/slurm/jobs/"+jobID, "en")

			assertStatus(t, response, http.StatusBadRequest)
			assertBodyContains(t, response, "Invalid request")
			if provider.jobCalls != 0 {
				t.Errorf("Job() calls = %d, want 0", provider.jobCalls)
			}
		})
	}
}

func TestSlurmJobDetailShowsNotFoundState(t *testing.T) {
	provider := &stubJobProvider{jobs: []cluster.Job{{ID: "32940", Name: "other-job", User: "user", State: "RUNNING"}}}
	handler := newSlurmPageHandler(t, nil, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs/32941", "zh")

	assertStatus(t, response, http.StatusNotFound)
	assertBodyContains(t, response, "当前队列中未找到该作业")
	assertBodyContains(t, response, `href="/slurm/jobs"`)
	assertBodyNotContains(t, response, "other-job")
}

func TestSlurmJobDetailDegradesWithoutLeakingProviderErrors(t *testing.T) {
	var logs bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousLogWriter) })
	provider := &stubJobProvider{err: errors.New("exec /secret/slurm: credential material")}
	handler := newSlurmPageHandler(t, nil, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs/32940", "en")

	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "Slurm data is temporarily unavailable")
	assertBodyNotContains(t, response, "/secret/slurm")
	assertBodyNotContains(t, response, "credential material")
	if strings.Contains(logs.String(), "/secret/slurm") || strings.Contains(logs.String(), "credential material") {
		t.Errorf("logs leaked provider error: %q", logs.String())
	}
}

func TestSlurmJobDetailRejectsMismatchedProviderRecord(t *testing.T) {
	handler := newSlurmPageHandler(t, nil, mismatchedJobProvider{})
	response := getAuthenticated(t, handler, "/slurm/jobs/32940", "en")

	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "Slurm data is temporarily unavailable")
	assertBodyNotContains(t, response, "wrong-job")
}

func TestSlurmJobDetailShowsChineseCopyForValidJob(t *testing.T) {
	const maxJobID = "9223372036854775807"
	provider := &stubJobProvider{jobs: []cluster.Job{{
		ID: maxJobID, Name: "训练任务", User: "researcher", Account: "jfzx", State: "PENDING",
		Elapsed: "—", TimeLimit: "1:00:00", NodeCount: 1, NodesOrReason: "Resources",
	}}}
	handler := newSlurmPageHandler(t, nil, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs/"+maxJobID, "zh")

	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"作业详情", "返回作业列表", "名称", "用户", "账户", "状态", "已运行", "时间限制", "节点数", "节点 / 原因", maxJobID} {
		assertBodyContains(t, response, value)
	}
}

func TestSlurmJobDetailRequiresAuthentication(t *testing.T) {
	handler := newSlurmPageHandler(t, nil, &stubJobProvider{})
	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32940", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/login?next=%2Fslurm%2Fjobs%2F32940")
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
	jobs      []cluster.Job
	err       error
	jobCalls  int
	lastJobID int64
}

func (p *stubJobProvider) Jobs(context.Context) ([]cluster.Job, error) {
	return append([]cluster.Job(nil), p.jobs...), p.err
}

func (p *stubJobProvider) Job(_ context.Context, id int64) (cluster.Job, bool, error) {
	p.jobCalls++
	p.lastJobID = id
	if p.err != nil {
		return cluster.Job{}, false, p.err
	}
	for _, job := range p.jobs {
		if job.ID == strconv.FormatInt(id, 10) {
			return job, true, nil
		}
	}
	return cluster.Job{}, false, nil
}

type mismatchedJobProvider struct{}

func (mismatchedJobProvider) Jobs(context.Context) ([]cluster.Job, error) {
	return nil, nil
}

func (mismatchedJobProvider) Job(context.Context, int64) (cluster.Job, bool, error) {
	return cluster.Job{ID: "32941", Name: "wrong-job", User: "user", State: "RUNNING"}, true, nil
}
