package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func TestJobResourceUsageReturnsAuthenticatedJSON(t *testing.T) {
	jobs := &stubJobProvider{jobs: []cluster.Job{{ID: "32943", State: "RUNNING"}}}
	resources := &stubJobResourceProvider{usage: cluster.JobResourceUsage{
		JobID: "32943", SampledAt: "2026-07-18T13:34:41Z", TotalCPUSeconds: 615, MaxRSSBytes: 2 << 20,
		Steps: []cluster.JobResourceStep{{Step: "batch", AveCPU: "00:10:15", AveCPUSeconds: 615, MaxRSS: "2M", MaxRSSBytes: 2 << 20}},
	}}
	handler := newJobResourceHandler(t, jobs, resources)
	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/resources", nil)
	request.Header.Set("Accept", "application/json")
	request.AddCookie(login(t, handler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("Cache-Control = %q", cacheControl)
	}
	var got cluster.JobResourceUsage
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.JobID != "32943" || got.TotalCPUSeconds != 615 || len(got.Steps) != 1 {
		t.Errorf("usage = %#v", got)
	}
	if jobs.jobCalls != 1 || resources.calls != 1 || resources.lastJobID != 32943 {
		t.Errorf("calls = jobs %d, resources %d (%d)", jobs.jobCalls, resources.calls, resources.lastJobID)
	}
}

func TestJobResourceUsageValidatesBeforeProviders(t *testing.T) {
	jobs := &stubJobProvider{}
	resources := &stubJobResourceProvider{}
	handler := newJobResourceHandler(t, jobs, resources)
	for _, path := range []string{"/slurm/jobs/0/resources", "/slurm/jobs/-1/resources", "/slurm/jobs/not-a-number/resources"} {
		response := getAuthenticated(t, handler, path, "zh")
		assertStatus(t, response, http.StatusBadRequest)
	}
	if jobs.jobCalls != 0 || resources.calls != 0 {
		t.Errorf("providers called for invalid IDs: jobs=%d resources=%d", jobs.jobCalls, resources.calls)
	}
}

func TestJobResourceUsageHandlesAccessAndProviderFailures(t *testing.T) {
	tests := []struct {
		name      string
		jobs      *stubJobProvider
		resources cluster.JobResourceProvider
		status    int
	}{
		{name: "missing job", jobs: &stubJobProvider{}, resources: &stubJobResourceProvider{}, status: http.StatusNotFound},
		{name: "job lookup failed", jobs: &stubJobProvider{err: errors.New("secret lookup")}, resources: &stubJobResourceProvider{}, status: http.StatusServiceUnavailable},
		{name: "resource unavailable", jobs: &stubJobProvider{jobs: []cluster.Job{{ID: "32943"}}}, resources: &stubJobResourceProvider{err: errors.New("secret sstat")}, status: http.StatusServiceUnavailable},
		{name: "provider disabled", jobs: &stubJobProvider{jobs: []cluster.Job{{ID: "32943"}}}, status: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newJobResourceHandler(t, test.jobs, test.resources)
			response := getAuthenticated(t, handler, "/slurm/jobs/32943/resources", "zh")
			assertStatus(t, response, test.status)
			assertBodyNotContains(t, response, "secret")
		})
	}
}

func TestJobResourceUsageRequiresAuthentication(t *testing.T) {
	handler := newJobResourceHandler(t, &stubJobProvider{}, &stubJobResourceProvider{})
	request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/resources", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
}

func TestJobResourceUsageLimitsConcurrentSstatCalls(t *testing.T) {
	started := make(chan struct{}, maxConcurrentJobResourceReads)
	release := make(chan struct{})
	resources := &blockingJobResourceProvider{started: started, release: release}
	handler := newJobResourceHandler(t, staticJobProvider{}, resources)
	cookie := login(t, handler)
	responses := make(chan int, maxConcurrentJobResourceReads)
	for range maxConcurrentJobResourceReads {
		go func() {
			request := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/resources", nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			responses <- response.Code
		}()
	}
	for range maxConcurrentJobResourceReads {
		<-started
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	overflow := httptest.NewRequest(http.MethodGet, "/slurm/jobs/32943/resources", nil)
	overflow.AddCookie(cookie)
	overflowResponse := httptest.NewRecorder()
	handler.ServeHTTP(overflowResponse, overflow)
	assertStatus(t, overflowResponse, http.StatusTooManyRequests)
	close(release)
	released = true
	for range maxConcurrentJobResourceReads {
		if status := <-responses; status != http.StatusOK {
			t.Errorf("resource response status = %d", status)
		}
	}
}

func newJobResourceHandler(t *testing.T, jobs cluster.JobProvider, resources cluster.JobResourceProvider) http.Handler {
	t.Helper()
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword,
		JobProvider: jobs, JobResourceProvider: resources,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

type stubJobResourceProvider struct {
	usage     cluster.JobResourceUsage
	err       error
	calls     int
	lastJobID int64
}

func (p *stubJobResourceProvider) JobResourceUsage(_ context.Context, id int64) (cluster.JobResourceUsage, error) {
	p.calls++
	p.lastJobID = id
	return p.usage, p.err
}

type staticJobProvider struct{}

func (staticJobProvider) Jobs(context.Context) ([]cluster.Job, error) {
	return []cluster.Job{{ID: "32943", State: "RUNNING"}}, nil
}

func (staticJobProvider) Job(_ context.Context, id int64) (cluster.Job, bool, error) {
	return cluster.Job{ID: "32943", State: "RUNNING"}, id == 32943, nil
}

type blockingJobResourceProvider struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (p *blockingJobResourceProvider) JobResourceUsage(_ context.Context, id int64) (cluster.JobResourceUsage, error) {
	p.started <- struct{}{}
	<-p.release
	return cluster.JobResourceUsage{JobID: "32943", Steps: []cluster.JobResourceStep{}}, nil
}
