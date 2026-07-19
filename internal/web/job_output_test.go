package web

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/platform"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

func TestJobOutputReturnsProviderSelectedPlainText(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stdout := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(stdout, []byte("<script>alert(1)</script>\nlatest"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &stubJobProvider{jobs: []cluster.Job{{
		ID: "32943", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: stdout,
	}}}
	handler := newJobOutputHandler(t, provider)

	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/output/stdout?path=/etc/passwd", nil)
	request.AddCookie(login(t, handler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assertStatus(t, response, http.StatusOK)
	assertHeader(t, response, "Content-Type", "text/plain; charset=utf-8")
	assertHeader(t, response, "Cache-Control", "no-store")
	assertHeader(t, response, "X-Content-Type-Options", "nosniff")
	if got := response.Body.String(); got != "<script>alert(1)</script>\nlatest" {
		t.Errorf("body = %q", got)
	}
	if provider.jobCalls != 1 || provider.lastJobID != 32943 {
		t.Errorf("Job calls = %d, last ID = %d", provider.jobCalls, provider.lastJobID)
	}
}

func TestJobOutputRejectsInvalidRequestBeforeProvider(t *testing.T) {
	provider := &stubJobProvider{}
	databasePath := filepath.Join(t.TempDir(), "audit.db")
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword, DatabasePath: databasePath,
		JobProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	for _, path := range []string{
		"/slurm/jobs/0/output/stdout",
		"/slurm/jobs/-1/output/stdout",
		"/slurm/jobs/not-a-number/output/stdout",
		"/slurm/jobs/1.5/output/stdout",
		"/slurm/jobs/999999999999999999999999999/output/stdout",
		"/slurm/jobs/32943/output/command",
	} {
		response := getAuthenticated(t, handler, path, "zh")
		assertStatus(t, response, http.StatusBadRequest)
	}
	if provider.jobCalls != 0 {
		t.Errorf("Job() calls = %d, want 0", provider.jobCalls)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var auditCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action = 'slurm.job_output.invalid_request' AND outcome = 'denied'").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 6 {
		t.Errorf("invalid request audit count = %d, want 6", auditCount)
	}
}

func TestJobOutputRequiresAuthentication(t *testing.T) {
	provider := &stubJobProvider{}
	handler := newJobOutputHandler(t, provider)
	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/output/stdout", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	if provider.jobCalls != 0 {
		t.Errorf("Job() calls = %d, want 0", provider.jobCalls)
	}
}

func TestJobOutputReadsFromSlurmOutputPathWithoutConfiguredRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	if err := os.WriteFile(path, []byte("work directory output"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &stubJobProvider{jobs: []cluster.Job{{ID: "32943", UserID: int64(os.Getuid()), WorkDir: "/", StdOut: path}}}
	handler := newJobOutputHandler(t, provider)
	page := getAuthenticated(t, handler, "/slurm/jobs", "zh")
	assertBodyContains(t, page, `data-output-label="标准输出">查看内容`)
	assertBodyContains(t, page, `data-output-label="标准错误">查看内容`)
	assertBodyNotContains(t, page, `data-output-label="标准输出" disabled`)
	assertBodyNotContains(t, page, `aria-describedby="job-output-`)
	assertBodyNotContains(t, page, "未启用输出内容预览")

	response := getAuthenticated(t, handler, "/slurm/jobs/32943/output/stdout", "zh")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "work directory output")
	if provider.jobCalls != 1 {
		t.Errorf("Job() calls = %d, want 1", provider.jobCalls)
	}
}

func TestJobOutputReadsConfiguredAbsolutePathsAndRejectsUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configuredPath := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(configuredPath, []byte("configured output"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(workDir, "link.log")
	if err := os.Symlink(configuredPath, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(workDir, "output.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		path           string
		status         int
		expectedOutput string
	}{
		{name: "configured absolute path", path: configuredPath, status: http.StatusOK, expectedOutput: "configured output"},
		{name: "relative path", path: "out.log", status: http.StatusNotFound},
		{name: "symlink", path: symlink, status: http.StatusNotFound},
		{name: "named pipe", path: fifo, status: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := cluster.Job{ID: "32943", UserID: int64(os.Getuid()), WorkDir: "/", StdOut: test.path}
			handler := newJobOutputHandler(t, &stubJobProvider{jobs: []cluster.Job{job}})
			response := getAuthenticated(t, handler, "/slurm/jobs/32943/output/stdout", "zh")
			assertStatus(t, response, test.status)
			if test.expectedOutput != "" {
				assertBodyContains(t, response, test.expectedOutput)
			}
		})
	}
}

func TestJobOutputRequiresTheJobUserFilesystemIdentity(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, []byte("readable by service"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		userID int64
		status int
	}{
		{userID: int64(os.Getuid()), status: http.StatusOK},
		{userID: int64(os.Getuid()) + 1, status: http.StatusNotFound},
	} {
		t.Run(strconv.FormatInt(test.userID, 10), func(t *testing.T) {
			job := cluster.Job{ID: "32943", UserID: test.userID, WorkDir: workDir, StdOut: path}
			response := getAuthenticated(t, newJobOutputHandler(t, &stubJobProvider{jobs: []cluster.Job{job}}), "/slurm/jobs/32943/output/stdout", "zh")
			assertStatus(t, response, test.status)
			if test.status == http.StatusOK {
				assertBodyContains(t, response, "readable by service")
			}
		})
	}
}

func TestJobOutputReturnsLatest256KiBAndRepairsInvalidUTF8(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "err.log")
	prefix := strings.Repeat("x", 32)
	tail := strings.Repeat("y", (256<<10)-2) + string([]byte{0xff}) + "z"
	if err := os.WriteFile(path, []byte(prefix+tail), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &stubJobProvider{jobs: []cluster.Job{{
		ID: "32943", UserID: int64(os.Getuid()), WorkDir: workDir, StdErr: path,
	}}}
	handler := newJobOutputHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs/32943/output/stderr", "zh")

	assertStatus(t, response, http.StatusOK)
	assertHeader(t, response, "X-Content-Truncated", "true")
	if strings.Contains(response.Body.String(), prefix) {
		t.Error("response contains truncated prefix")
	}
	if !strings.Contains(response.Body.String(), "\uFFFDz") {
		t.Errorf("invalid UTF-8 was not repaired: %q", response.Body.String()[response.Body.Len()-8:])
	}
}

func TestJobOutputResponseStaysWithinLimitAfterUTF8Repair(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0xff, 'a'}, 128<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &stubJobProvider{jobs: []cluster.Job{{
		ID: "32943", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path,
	}}}
	response := getAuthenticated(t, newJobOutputHandler(t, provider), "/slurm/jobs/32943/output/stdout", "en")
	assertStatus(t, response, http.StatusOK)
	if response.Body.Len() > 256<<10 {
		t.Errorf("response bytes = %d, want <= %d", response.Body.Len(), 256<<10)
	}
	if !strings.Contains(response.Body.String(), "\uFFFD") {
		t.Error("invalid UTF-8 was not repaired")
	}
}

func TestJobOutputButtonsDoNotRequirePerStreamMetadata(t *testing.T) {
	root := t.TempDir()
	provider := &stubJobProvider{jobs: []cluster.Job{{ID: "32943", UserID: int64(os.Getuid()), WorkDir: root, StdOut: filepath.Join(root, "out.log")}}}
	page := getAuthenticated(t, newJobOutputHandler(t, provider), "/slurm/jobs", "en")
	assertStatus(t, page, http.StatusOK)
	assertBodyContains(t, page, `data-output-stream="stdout"`)
	assertBodyContains(t, page, `data-output-stream="stderr" data-output-label="Standard error">View content`)
}

func TestJobOutputAllowsOwnersButDeniesOtherUsers(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, []byte("owner output"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("ordinary user password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), platform.PlatformUser{Username: "alice", PasswordHash: string(passwordHash), Role: platform.RoleUser, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword, PlatformUsers: store,
		JobProvider: &stubJobProvider{jobs: []cluster.Job{
			{ID: "32943", User: "alice", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path},
			{ID: "32944", User: "bob", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	login := postForm(handler, "/login", url.Values{"username": {"alice"}, "password": {"ordinary user password"}}, nil)
	assertStatus(t, login, http.StatusSeeOther)
	session := findCookie(t, login.Result().Cookies(), sessionCookie)

	for _, test := range []struct {
		path   string
		status int
	}{
		{path: "/slurm/jobs/32943/output/stdout", status: http.StatusOK},
		{path: "/slurm/jobs/32944/output/stdout", status: http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.AddCookie(session)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, test.status)
	}
}

func TestJobOutputAllowsPlatformAdminToReadOtherUserOutput(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, []byte("administrator output"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := platform.OpenUserStore(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("operator password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(context.Background(), platform.PlatformUser{Username: "operator", PasswordHash: string(passwordHash), Role: platform.RoleAdmin, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword, PlatformUsers: store,
		JobProvider: &stubJobProvider{jobs: []cluster.Job{{ID: "32943", User: "alice", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	login := postForm(handler, "/login", url.Values{"username": {"operator"}, "password": {"operator password"}}, nil)
	assertStatus(t, login, http.StatusSeeOther)
	session := findCookie(t, login.Result().Cookies(), sessionCookie)
	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/output/stdout", nil)
	request.AddCookie(session)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "administrator output")
}

func TestJobOutputFrontendHandlesStaleAndTruncatedResponses(t *testing.T) {
	handler := newJobOutputHandler(t, &stubJobProvider{})
	request := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "outputRequestID")
	assertBodyContains(t, response, "AbortController")
	assertBodyContains(t, response, ".abort()")
	assertBodyContains(t, response, "X-Content-Truncated")
}

func TestJobOutputConcurrencyLimitRecoversAfterReadsFinish(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := &blockingJobProvider{
		job:     cluster.Job{ID: "32943", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path},
		entered: make(chan struct{}, maxConcurrentJobOutputReads), release: make(chan struct{}),
	}
	handler := newJobOutputHandler(t, provider)
	cookie := login(t, handler)

	responses := make(chan int, maxConcurrentJobOutputReads)
	var requests sync.WaitGroup
	for range maxConcurrentJobOutputReads {
		requests.Add(1)
		go func() {
			defer requests.Done()
			responses <- requestJobOutput(handler, cookie).Code
		}()
	}
	for range maxConcurrentJobOutputReads {
		<-provider.entered
	}
	assertStatus(t, requestJobOutput(handler, cookie), http.StatusTooManyRequests)

	close(provider.release)
	requests.Wait()
	close(responses)
	for status := range responses {
		if status != http.StatusOK {
			t.Errorf("blocked request status = %d, want 200", status)
		}
	}
	assertStatus(t, requestJobOutput(handler, cookie), http.StatusOK)
}

func TestJobOutputAuditSurvivesRequestCancellation(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, []byte("cancelled"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "audit.db")
	provider := &stubJobProvider{jobs: []cluster.Job{{ID: "32943", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path}}}
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword, DatabasePath: databasePath,
		JobProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	cookie := login(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/output/stdout", nil)
	request.AddCookie(cookie)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request.WithContext(ctx))
	if response.Code == http.StatusOK {
		t.Fatal("cancelled request unexpectedly returned output")
	}

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var outcome string
	if err := database.QueryRow("SELECT outcome FROM audit_events WHERE action = 'slurm.job_output.stdout:32943'").Scan(&outcome); err != nil {
		t.Fatalf("query cancelled output audit: %v", err)
	}
	if outcome != "cancelled" {
		t.Errorf("cancelled output outcome = %q, want cancelled", outcome)
	}
}

func TestReadJobOutputPreservesCancellation(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, []byte("cancelled"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := readJobOutput(ctx, cluster.Job{
		UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path,
	}, "stdout")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("readJobOutput error = %v, want context.Canceled", err)
	}
}

func TestJobOutputRecordsSuccessfulAuditEvent(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, "out.log")
	if err := os.WriteFile(path, []byte("audited"), 0o600); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "audit.db")
	provider := &stubJobProvider{jobs: []cluster.Job{{ID: "32943", UserID: int64(os.Getuid()), WorkDir: workDir, StdOut: path}}}
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword, DatabasePath: databasePath,
		JobProvider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	response := getAuthenticated(t, handler, "/slurm/jobs/32943/output/stdout", "en")
	assertStatus(t, response, http.StatusOK)

	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var actor, action, outcome string
	if err := database.QueryRow("SELECT actor, action, outcome FROM audit_events WHERE action LIKE 'slurm.job_output.%'").Scan(&actor, &action, &outcome); err != nil {
		t.Fatalf("query output audit: %v", err)
	}
	if actor != testUsername || action != "slurm.job_output.stdout:32943" || outcome != "success" {
		t.Errorf("audit = (%q, %q, %q)", actor, action, outcome)
	}
}

func TestJobOutputProviderErrorsAreRedacted(t *testing.T) {
	provider := &stubJobProvider{err: errors.New("/secret/slurm token=credential")}
	handler := newJobOutputHandler(t, provider)
	response := getAuthenticated(t, handler, "/slurm/jobs/32943/output/stdout", "zh")
	assertStatus(t, response, http.StatusServiceUnavailable)
	assertBodyNotContains(t, response, "/secret/slurm")
	assertBodyNotContains(t, response, "credential")
}

func TestJobOutputFailureLogsStageWithoutOutputPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-output.log")
	provider := &stubJobProvider{jobs: []cluster.Job{{
		ID: "32943", UserID: int64(os.Getuid()), StdOut: path,
	}}}
	handler := newJobOutputHandler(t, provider)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)

	response := getAuthenticated(t, handler, "/slurm/jobs/32943/output/stdout", "zh")
	assertStatus(t, response, http.StatusNotFound)
	assertBodyNotContains(t, response, path)
	if !strings.Contains(output.String(), "job output read failed: job_id=32943 stream=stdout user_id=") {
		t.Errorf("log = %q, want failure stage", output.String())
	}
	if strings.Contains(output.String(), path) {
		t.Errorf("log leaked output path: %q", output.String())
	}
}

func TestJobOutputFrontendUsesTextContent(t *testing.T) {
	handler := newJobOutputHandler(t, &stubJobProvider{})
	request := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, ".textContent")
	assertBodyNotContains(t, response, ".innerHTML")
	assertBodyContains(t, response, "event.key === 'Escape'")
	assertBodyContains(t, response, "normalizeOutputLineEndings")
	assertBodyContains(t, response, `replace(/\r\n?/g, '\n')`)
}

func TestJobOutputDuplicateFileDescriptorsAreCloseOnExec(t *testing.T) {
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rootFD)
	duplicate, err := duplicateCloseOnExec(rootFD)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(duplicate)
	flags, err := unix.FcntlInt(uintptr(duplicate), unix.F_GETFD, 0)
	if err != nil {
		t.Fatal(err)
	}
	if flags&unix.FD_CLOEXEC == 0 {
		t.Errorf("descriptor flags = %#x, want FD_CLOEXEC", flags)
	}
}

func newJobOutputHandler(t *testing.T, jobs cluster.JobProvider) http.Handler {
	t.Helper()
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword,
		JobProvider: jobs,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

func TestJobOutputRejectsProviderIDMismatch(t *testing.T) {
	handler := newJobOutputHandler(t, mismatchedJobProvider{})
	response := getAuthenticated(t, handler, "/slurm/jobs/"+strconv.FormatInt(32943, 10)+"/output/stdout", "zh")
	assertStatus(t, response, http.StatusNotFound)
}

func requestJobOutput(handler http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/output/stdout", nil)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type blockingJobProvider struct {
	job     cluster.Job
	entered chan struct{}
	release chan struct{}
}

func (p *blockingJobProvider) Jobs(context.Context) ([]cluster.Job, error) {
	return []cluster.Job{p.job}, nil
}

func (p *blockingJobProvider) Job(context.Context, int64) (cluster.Job, bool, error) {
	p.entered <- struct{}{}
	<-p.release
	return p.job, true, nil
}
