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
	provider := &stubJobProvider{jobs: []cluster.Job{{
		ID: "32943", Name: "<script>Emilia</script>", User: "liyuxiang", UserID: 10001, Account: "jfzx", Partition: "GPU<fast>",
		State: "RUNNING", CPUCount: 16, Elapsed: "02:14:45", TimeLimit: "UNLIMITED", NodeCount: 1, Nodes: "node31", NodesOrReason: "node31",
		SubmitTime: "2026-07-18T13:34:41", EligibleTime: "2026-07-18T13:34:41", StartTime: "2026-07-18T13:34:41", EndTime: "Unknown",
		WorkDir: "/home/liyuxiangjfzx/work/PVA-MPN/20", StdOut: "/home/liyuxiangjfzx/work/PVA-MPN/20/out_32943.log",
		StdErr: "/home/liyuxiangjfzx/work/PVA-MPN/20/err_32943.log", Command: "/home/liyuxiangjfzx/work/PVA-MPN/20/md_gpu.slurm",
	}}}
	handler := newSlurmPageHandler(t, nil, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		"作业管理", "分区", "CPU数", "操作", "详情", "32943", "&lt;script&gt;Emilia&lt;/script&gt;", "liyuxiang(10001)", "GPU&lt;fast&gt;", "RUNNING", "node31", "16",
		"作业详细信息", "提交时间", "可调度时间=2026-07-18T13:34:41", "开始时间", "结束时间=未知", "工作目录",
		"标准输出", "标准错误", "提交命令", "/home/liyuxiangjfzx/work/PVA-MPN/20/md_gpu.slurm", "查看内容",
	} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "<script>Emilia</script>")
	assertBodyContains(t, response, `class="app-shell"`)
	assertBodyContains(t, response, `<dialog id="job-detail-modal"`)
	assertBodyContains(t, response, `aria-labelledby="job-detail-title"`)
	assertBodyContains(t, response, `data-job-detail="job-detail-32943"`)
	assertBodyContains(t, response, `aria-haspopup="dialog"`)
	assertBodyContains(t, response, `aria-controls="job-detail-modal"`)
	assertBodyContains(t, response, `aria-label="详情: 32943"`)
	assertBodyContains(t, response, `aria-label="关闭"`)
	assertBodyContains(t, response, `<tr><td><strong>32943</strong></td>`)
	assertBodyContains(t, response, `class="job-detail-button"`)
	assertBodyContains(t, response, `class="job-resource-button"`)
	assertBodyContains(t, response, `data-job-resource="32943"`)
	assertBodyContains(t, response, `<dialog id="job-resource-modal"`)
	assertBodyContains(t, response, `data-resource-chart`)
	assertBodyContains(t, response, `data-sstat-table`)
	assertBodyNotContains(t, response, `href="/slurm/jobs/32943"`)
	if count := strings.Count(response.Body.String(), `<dialog id="job-detail-modal"`); count != 1 {
		t.Errorf("job detail dialogs = %d, want 1", count)
	}
}

func TestSlurmJobDetailHasNoStandalonePage(t *testing.T) {
	handler := newSlurmPageHandler(t, nil, &stubJobProvider{})
	response := getAuthenticated(t, handler, "/slurm/jobs/32943", "zh")
	assertStatus(t, response, http.StatusNotFound)
	assertBodyNotContains(t, response, `data-component="app-shell"`)
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

func TestSlurmJobsProviderErrorLogIsRedacted(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	handler := newSlurmPageHandler(t, nil, &stubJobProvider{err: errors.New("/secret/slurm credential material")})
	response := getAuthenticated(t, handler, "/slurm/jobs", "zh")
	assertStatus(t, response, http.StatusOK)
	if strings.Contains(output.String(), "/secret/slurm") || strings.Contains(output.String(), "credential material") {
		t.Errorf("provider error leaked to logs: %q", output.String())
	}
	if !strings.Contains(output.String(), "Slurm jobs snapshot failed") {
		t.Errorf("redacted failure log missing: %q", output.String())
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
