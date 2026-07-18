package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

const (
	testUsername = "admin"
	testPassword = "correct horse battery staple"
)

func TestLoginPage(t *testing.T) {
	handler := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, `action="/login"`)
	assertBodyContains(t, response, `name="username"`)
	assertBodyContains(t, response, `name="password"`)
}

func TestLoginAuthenticatesValidCredentials(t *testing.T) {
	handler := newTestHandler(t)

	response := postForm(handler, "/login", url.Values{
		"username": {testUsername},
		"password": {testPassword},
	}, nil)

	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/dashboard")

	session := findCookie(t, response.Result().Cookies(), "openhpc_session")
	if session.Value == "" {
		t.Fatal("session cookie must have a non-empty value")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Errorf("session cookie SameSite = %v, want Lax", session.SameSite)
	}
	csrf := findCookie(t, response.Result().Cookies(), "openhpc_csrf")
	if csrf.Value == "" {
		t.Fatal("CSRF cookie must have a non-empty value")
	}
	if csrf.SameSite != http.SameSiteStrictMode {
		t.Errorf("CSRF cookie SameSite = %v, want Strict", csrf.SameSite)
	}
}

func TestLoginRejectsInvalidCredentialsWithoutCreatingSession(t *testing.T) {
	handler := newTestHandler(t)

	response := postForm(handler, "/login", url.Values{
		"username": {testUsername},
		"password": {"wrong"},
	}, nil)

	assertStatus(t, response, http.StatusUnauthorized)
	assertBodyContains(t, response, "用户名或密码错误")
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "openhpc_session" && cookie.Value != "" {
			t.Fatal("invalid login must not create a session")
		}
	}
}

func TestLoginRejectsOversizedCredentialsWithoutCreatingSession(t *testing.T) {
	tests := []struct {
		name     string
		username string
		password string
	}{
		{name: "username over 64 bytes", username: strings.Repeat("a", 65), password: testPassword},
		{name: "password over 256 bytes", username: testUsername, password: strings.Repeat("a", 257)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestHandler(t)
			response := postForm(handler, "/login", url.Values{
				"username": {test.username},
				"password": {test.password},
			}, nil)

			assertStatus(t, response, http.StatusBadRequest)
			assertNoActiveSessionCookie(t, response)
		})
	}
}

func TestLoginRateLimitsRepeatedFailuresFromSameRemoteAddress(t *testing.T) {
	handler := newTestHandler(t)
	const remoteAddress = "203.0.113.42:43120"

	for attempt := 1; attempt <= 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{
			"username": {testUsername},
			"password": {"wrong"},
		}.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.RemoteAddr = remoteAddress
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		want := http.StatusUnauthorized
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		assertStatus(t, response, want)
		assertNoActiveSessionCookie(t, response)
	}
}

func TestLoginRateLimitIsAtomicForConcurrentFailures(t *testing.T) {
	handler := newTestHandler(t)
	const (
		requestCount  = 20
		remoteAddress = "203.0.113.42:43120"
	)

	start := make(chan struct{})
	statuses := make(chan int, requestCount)
	var workers sync.WaitGroup
	workers.Add(requestCount)
	for range requestCount {
		go func() {
			defer workers.Done()
			<-start
			request := newFailedLoginRequest(remoteAddress, "")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			statuses <- response.Code
		}()
	}
	close(start)
	workers.Wait()
	close(statuses)

	unauthorized := 0
	tooManyRequests := 0
	for status := range statuses {
		switch status {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			tooManyRequests++
		default:
			t.Errorf("concurrent failed login status = %d, want 401 or 429", status)
		}
	}
	if unauthorized > 5 {
		t.Errorf("unauthorized responses = %d, want at most 5", unauthorized)
	}
	if tooManyRequests < requestCount-5 {
		t.Errorf("rate-limited responses = %d, want at least %d", tooManyRequests, requestCount-5)
	}
}

func TestAdminUsernameIsNormalizedAtConfigurationBoundary(t *testing.T) {
	handler, err := New(Config{
		AdminUsername: " admin ",
		AdminPassword: testPassword,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)

	response := postForm(handler, "/login", url.Values{
		"username": {"admin"},
		"password": {testPassword},
	}, nil)

	assertStatus(t, response, http.StatusSeeOther)
	assertHeader(t, response, "Location", "/dashboard")
	findCookie(t, response.Result().Cookies(), sessionCookie)
}

func TestTrustedProxyUsesForwardedClientAddressesForIndependentRateLimits(t *testing.T) {
	handler, err := New(Config{
		AdminUsername:     testUsername,
		AdminPassword:     testPassword,
		TrustedProxyCIDRs: []string{"127.0.0.0/8"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)

	const proxyAddress = "127.0.0.1:43120"
	for attempt := 1; attempt <= 5; attempt++ {
		for _, clientAddress := range []string{"198.51.100.10", "198.51.100.20"} {
			request := newFailedLoginRequest(proxyAddress, clientAddress)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertStatus(t, response, http.StatusUnauthorized)
		}
	}

	for _, clientAddress := range []string{"198.51.100.10", "198.51.100.20"} {
		request := newFailedLoginRequest(proxyAddress, clientAddress)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, http.StatusTooManyRequests)
	}
}

func TestUntrustedForwardedForCannotBypassDirectAddressRateLimit(t *testing.T) {
	handler := newTestHandler(t)
	const remoteAddress = "203.0.113.42:43120"

	forwardedAddresses := []string{
		"198.51.100.1", "198.51.100.2", "198.51.100.3",
		"198.51.100.4", "198.51.100.5", "198.51.100.6",
	}
	for index, forwardedFor := range forwardedAddresses {
		request := newFailedLoginRequest(remoteAddress, forwardedFor)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		want := http.StatusUnauthorized
		if index == len(forwardedAddresses)-1 {
			want = http.StatusTooManyRequests
		}
		assertStatus(t, response, want)
	}
}

func TestLogoutClearsAndInvalidatesSession(t *testing.T) {
	handler := newTestHandler(t)
	session, csrf := loginWithCSRF(t, handler)

	logoutResponse := postProtectedForm(handler, "/logout", url.Values{}, session, csrf)
	assertStatus(t, logoutResponse, http.StatusSeeOther)
	assertHeader(t, logoutResponse, "Location", "/login")
	clearedSession := findCookie(t, logoutResponse.Result().Cookies(), sessionCookie)
	if clearedSession.Value != "" {
		t.Errorf("cleared session cookie value = %q, want empty", clearedSession.Value)
	}
	if clearedSession.MaxAge >= 0 {
		t.Errorf("cleared session cookie MaxAge = %d, want negative", clearedSession.MaxAge)
	}

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	dashboardRequest.AddCookie(session)
	dashboardResponse := httptest.NewRecorder()
	handler.ServeHTTP(dashboardResponse, dashboardRequest)

	assertStatus(t, dashboardResponse, http.StatusFound)
	assertHeader(t, dashboardResponse, "Location", "/login?next="+url.QueryEscape("/dashboard"))
}

func TestFailedLoginPreservesNextForSubsequentSuccess(t *testing.T) {
	handler := newTestHandler(t)
	const next = "/slurm/jobs"

	failedResponse := postForm(handler, "/login", url.Values{
		"username": {testUsername},
		"password": {"wrong"},
		"next":     {next},
	}, nil)
	assertStatus(t, failedResponse, http.StatusUnauthorized)
	assertBodyContains(t, failedResponse, `name="next" value="/slurm/jobs"`)

	successResponse := postForm(handler, "/login", url.Values{
		"username": {testUsername},
		"password": {testPassword},
		"next":     {next},
	}, nil)
	assertStatus(t, successResponse, http.StatusSeeOther)
	assertHeader(t, successResponse, "Location", next)
	findCookie(t, successResponse.Result().Cookies(), sessionCookie)
}

func TestProtectedPagesRedirectAnonymousUsersToLogin(t *testing.T) {
	handler := newTestHandler(t)

	for _, path := range []string{"/", "/dashboard"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			assertStatus(t, response, http.StatusFound)
			assertHeader(t, response, "Location", "/login?next="+url.QueryEscape(path))
		})
	}
}

func TestDashboardShowsCoreMetricsAndNavigation(t *testing.T) {
	handler := newTestHandler(t)
	session := login(t, handler)

	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.AddCookie(session)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assertStatus(t, response, http.StatusOK)
	for _, expected := range []string{
		`lang="zh"`,
		`data-theme="research-red"`,
		"集群概览",
		"在线节点", "12",
		"运行作业", "34",
		"排队作业", "5",
		"CPU 利用率", "67%",
		"平台管理员",
		"运维操作",
		`aria-label="主导航"`,
		`aria-label="菜单"`,
		`aria-label="CPU 和 GPU 利用率图表"`,
		`href="/dashboard"`,
		`href="/slurm/jobs"`,
		`href="/slurm/nodes"`,
		`href="/slurm/accounts"`,
		`href="/system/files"`,
		`href="/terminal"`,
	} {
		assertBodyContains(t, response, expected)
	}
	for _, untranslated := range []string{"Platform admin", "OPERATIONS", `aria-label="Primary"`, `aria-label="Menu"`, `aria-label="CPU and GPU utilization chart"`} {
		assertBodyNotContains(t, response, untranslated)
	}
}

func TestProtectedPagesShareApplicationChrome(t *testing.T) {
	handler := newTestHandler(t)
	session := login(t, handler)
	tests := []struct {
		path         string
		heading      string
		active       string
		expectedCSRF int
	}{
		{path: "/dashboard", heading: "集群概览", active: "/dashboard", expectedCSRF: 3},
		{path: "/slurm/nodes", heading: "节点与分区", active: "/slurm/nodes", expectedCSRF: 3},
		{path: "/slurm/jobs", heading: "作业管理", active: "/slurm/jobs", expectedCSRF: 3},
		{path: "/ldap", heading: "LDAP 目录", active: "/ldap", expectedCSRF: 4},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.AddCookie(session)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			assertStatus(t, response, http.StatusOK)
			for _, expected := range []string{
				`data-component="app-shell"`,
				`data-component="app-sidebar"`,
				`data-component="app-topbar"`,
				`data-component="page-heading"`,
				`action="/logout"`,
				`<title>OpenHPC Web · ` + test.heading + `</title>`,
				`href="` + test.active + `" class="nav-item active" aria-current="page"`,
				`aria-controls="sidebar" aria-expanded="false"`,
				"<h1>" + test.heading + "</h1>",
			} {
				assertBodyContains(t, response, expected)
			}
			if csrfFields := regexp.MustCompile(`name="_csrf" value="[^"]+"`).FindAllString(response.Body.String(), -1); len(csrfFields) != test.expectedCSRF {
				t.Errorf("populated CSRF fields = %d, want %d", len(csrfFields), test.expectedCSRF)
			}
		})
	}
}

func TestModuleChromeReflectsMetricsProviderHealth(t *testing.T) {
	tests := []struct {
		name        string
		provider    *stubMetricsProvider
		status      string
		unavailable bool
	}{
		{name: "available", provider: &stubMetricsProvider{metrics: cluster.Metrics{CPUUsage: 42}}, status: "当前调度快照"},
		{name: "unavailable", provider: &stubMetricsProvider{err: errors.New("exec /secret/sinfo: provider failed")}, status: "Slurm 数据暂不可用", unavailable: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, MetricsProvider: test.provider})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			cleanupHandler(t, handler)

			response := getAuthenticated(t, handler, "/ldap", "zh")
			assertStatus(t, response, http.StatusOK)
			assertBodyContains(t, response, test.status)
			if test.unavailable {
				assertBodyContains(t, response, `<span class="status-dot unavailable"></span>`)
				assertBodyNotContains(t, response, "/secret/sinfo")
				assertBodyNotContains(t, response, "provider failed")
			} else {
				assertBodyNotContains(t, response, `<span class="status-dot unavailable"></span>`)
			}
			if test.provider.calls != 1 {
				t.Errorf("Snapshot() calls = %d, want 1", test.provider.calls)
			}
		})
	}
}

func TestDashboardUsesLiveMetricsProvider(t *testing.T) {
	provider := &stubMetricsProvider{metrics: cluster.Metrics{
		OnlineNodes: 701,
		RunningJobs: 702,
		QueuedJobs:  703,
		CPUUsage:    73,
	}}
	handler, err := New(Config{
		AdminUsername:   testUsername,
		AdminPassword:   testPassword,
		MetricsProvider: provider,
		Metrics: cluster.Metrics{
			OnlineNodes: 91,
			RunningJobs: 92,
			QueuedJobs:  93,
			CPUUsage:    94,
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	session := login(t, handler)

	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.AddCookie(session)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assertStatus(t, response, http.StatusOK)
	for _, expected := range []string{
		`<strong>701</strong><p>在线节点</p>`,
		`<strong>702</strong><p>运行作业</p>`,
		`<strong>703</strong><p>排队作业</p>`,
		`<strong>73%</strong><p>CPU 利用率</p>`,
	} {
		assertBodyContains(t, response, expected)
	}
	if provider.calls != 1 {
		t.Errorf("Snapshot() calls = %d, want 1", provider.calls)
	}
}

func TestDashboardShowsUnavailableStateWithoutLeakingProviderError(t *testing.T) {
	providerError := errors.New("exec /opt/slurm/bin/sinfo: secret scheduler failure")
	tests := []struct {
		name        string
		language    string
		unavailable string
	}{
		{name: "Chinese", language: "zh", unavailable: "Slurm 数据暂不可用"},
		{name: "English", language: "en", unavailable: "Slurm data is temporarily unavailable"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &stubMetricsProvider{err: providerError}
			handler, err := New(Config{
				AdminUsername:   testUsername,
				AdminPassword:   testPassword,
				MetricsProvider: provider,
				Metrics: cluster.Metrics{
					OnlineNodes: 91,
					RunningJobs: 92,
					QueuedJobs:  93,
					CPUUsage:    94,
				},
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			cleanupHandler(t, handler)
			session := login(t, handler)

			request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
			request.AddCookie(session)
			if test.language == "en" {
				request.AddCookie(&http.Cookie{Name: "openhpc_language", Value: "en"})
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			assertStatus(t, response, http.StatusOK)
			assertBodyContains(t, response, test.unavailable)
			if count := strings.Count(response.Body.String(), `<strong>—</strong>`); count != 4 {
				t.Errorf("unavailable metric count = %d, want 4", count)
			}
			for _, leaked := range []string{"<strong>91</strong>", "<strong>92</strong>", "<strong>93</strong>", "<strong>94%</strong>", "/opt/slurm/bin/sinfo", "secret scheduler failure"} {
				assertBodyNotContains(t, response, leaked)
			}
		})
	}
}

func TestDashboardWithoutProviderIsUnavailable(t *testing.T) {
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	request.AddCookie(login(t, handler))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "Slurm 数据暂不可用")
	assertBodyNotContains(t, response, "所有系统运行正常")
}

func TestLanguagePreferenceSwitchesDashboardBetweenChineseAndEnglish(t *testing.T) {
	handler := newTestHandler(t)
	session, csrf := loginWithCSRF(t, handler)

	preferenceResponse := postProtectedForm(handler, "/preferences/language", url.Values{
		"language": {"en"},
	}, session, csrf)
	assertStatus(t, preferenceResponse, http.StatusSeeOther)
	assertHeader(t, preferenceResponse, "Location", "/dashboard")
	language := findCookie(t, preferenceResponse.Result().Cookies(), "openhpc_language")
	if language.Value != "en" {
		t.Fatalf("language cookie = %q, want en", language.Value)
	}

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	dashboardRequest.AddCookie(session)
	dashboardRequest.AddCookie(language)
	dashboardResponse := httptest.NewRecorder()
	handler.ServeHTTP(dashboardResponse, dashboardRequest)

	assertStatus(t, dashboardResponse, http.StatusOK)
	assertBodyContains(t, dashboardResponse, `lang="en"`)
	assertBodyContains(t, dashboardResponse, "Cluster Overview")
	assertBodyContains(t, dashboardResponse, "Running Jobs")
	assertBodyContains(t, dashboardResponse, "Platform admin")
	assertBodyContains(t, dashboardResponse, "Operations")
	assertBodyContains(t, dashboardResponse, `aria-label="Primary navigation"`)
	assertBodyContains(t, dashboardResponse, `aria-label="Menu"`)
	assertBodyContains(t, dashboardResponse, `aria-label="CPU and GPU utilization chart"`)
}

func TestLanguagePreferenceRejectsUnsupportedValues(t *testing.T) {
	handler := newTestHandler(t)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/preferences/language", url.Values{
		"language": {"fr"},
	}, session, csrf)

	assertStatus(t, response, http.StatusBadRequest)
}

func TestThemePreferenceSwitchesBetweenResearchRedAndSlurmBlue(t *testing.T) {
	handler := newTestHandler(t)
	session, csrf := loginWithCSRF(t, handler)

	preferenceResponse := postProtectedForm(handler, "/preferences/theme", url.Values{
		"theme": {"slurm-blue"},
	}, session, csrf)
	assertStatus(t, preferenceResponse, http.StatusSeeOther)
	assertHeader(t, preferenceResponse, "Location", "/dashboard")
	theme := findCookie(t, preferenceResponse.Result().Cookies(), "openhpc_theme")
	if theme.Value != "slurm-blue" {
		t.Fatalf("theme cookie = %q, want slurm-blue", theme.Value)
	}

	dashboardRequest := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	dashboardRequest.AddCookie(session)
	dashboardRequest.AddCookie(theme)
	dashboardResponse := httptest.NewRecorder()
	handler.ServeHTTP(dashboardResponse, dashboardRequest)

	assertStatus(t, dashboardResponse, http.StatusOK)
	assertBodyContains(t, dashboardResponse, `data-theme="slurm-blue"`)
	assertBodyContains(t, dashboardResponse, `value="research-red"`)
	assertBodyContains(t, dashboardResponse, `value="slurm-blue"`)
}

func TestThemePreferenceRejectsUnsupportedValues(t *testing.T) {
	handler := newTestHandler(t)
	session, csrf := loginWithCSRF(t, handler)

	response := postProtectedForm(handler, "/preferences/theme", url.Values{
		"theme": {"neon"},
	}, session, csrf)

	assertStatus(t, response, http.StatusBadRequest)
}

func TestPreferenceEndpointsRequireAuthentication(t *testing.T) {
	handler := newTestHandler(t)

	for _, path := range []string{"/preferences/language", "/preferences/theme"} {
		t.Run(path, func(t *testing.T) {
			response := postForm(handler, path, url.Values{}, nil)
			assertStatus(t, response, http.StatusFound)
			assertHeader(t, response, "Location", "/login?next="+url.QueryEscape(path))
		})
	}
}

func TestProtectedPostsRequireMatchingCSRFToken(t *testing.T) {
	handler := newTestHandler(t)
	session, csrf := loginWithCSRF(t, handler)

	tests := []struct {
		name   string
		values url.Values
	}{
		{name: "missing token", values: url.Values{"language": {"en"}}},
		{name: "incorrect token", values: url.Values{"language": {"en"}, "_csrf": {"incorrect"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := postFormWithCookies(handler, "/preferences/language", test.values, session, csrf)
			assertStatus(t, response, http.StatusForbidden)
		})
	}

	response := postProtectedForm(handler, "/preferences/language", url.Values{"language": {"en"}}, session, csrf)
	assertStatus(t, response, http.StatusSeeOther)
}

func TestRequestBodyLimitRejectsOversizedLogin(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(strings.Repeat("x", (16<<10)+1)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertStatus(t, response, http.StatusRequestEntityTooLarge)
	assertBodyContains(t, response, "请求内容过大")
}

func TestProtectedFormsAcceptBrowserOriginsWithValidCSRF(t *testing.T) {
	tests := []struct {
		name         string
		origin       string
		secFetchSite string
	}{
		{name: "null origin", origin: "null", secFetchSite: "cross-site"},
		{name: "application origin", origin: "codex://desktop"},
		{name: "matching web origin", origin: "http://example.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := newTestHandler(t)
			session, csrf := loginWithCSRF(t, handler)
			body := url.Values{"language": {"en"}, "_csrf": {csrf.Value}}.Encode()
			request := httptest.NewRequest(http.MethodPost, "http://example.com/preferences/language", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(session)
			request.AddCookie(csrf)
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.secFetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.secFetchSite)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			assertStatus(t, response, http.StatusSeeOther)
			assertHeader(t, response, "Location", "/dashboard")
		})
	}
}

func TestSecureCookiesApplyToSessionAndPreferences(t *testing.T) {
	handler, err := New(Config{
		AdminUsername: testUsername,
		AdminPassword: testPassword,
		SecureCookies: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)

	loginResponse := postForm(handler, "/login", url.Values{
		"username": {testUsername},
		"password": {testPassword},
	}, nil)
	assertStatus(t, loginResponse, http.StatusSeeOther)
	session := findCookie(t, loginResponse.Result().Cookies(), sessionCookie)
	csrf := findCookie(t, loginResponse.Result().Cookies(), "openhpc_csrf")
	if !session.Secure || !csrf.Secure {
		t.Errorf("authentication cookies Secure = (%v, %v), want both true", session.Secure, csrf.Secure)
	}

	for path, values := range map[string]url.Values{
		"/preferences/language": {"language": {"en"}},
		"/preferences/theme":    {"theme": {"slurm-blue"}},
	} {
		response := postProtectedForm(handler, path, values, session, csrf)
		assertStatus(t, response, http.StatusSeeOther)
		preference := response.Result().Cookies()[0]
		if !preference.Secure {
			t.Errorf("%s cookie Secure = false, want true", path)
		}
	}
}

func TestNewRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	handler, err := New(Config{
		AdminUsername:     testUsername,
		AdminPassword:     testPassword,
		TrustedProxyCIDRs: []string{"not-a-cidr"},
	})
	if err == nil {
		if handler != nil {
			cleanupHandler(t, handler)
		}
		t.Fatal("New() error = nil, want invalid trusted proxy error")
	}
	if handler != nil {
		t.Error("New() returned handler with invalid trusted proxy CIDR")
	}
	if !strings.Contains(err.Error(), "parse trusted proxy CIDR") {
		t.Errorf("New() error = %q, want trusted proxy context", err)
	}
}

func TestPublicErrorMessage(t *testing.T) {
	tests := []struct {
		code int
		zh   string
		en   string
	}{
		{code: http.StatusBadRequest, zh: "请求参数无效", en: "Invalid request"},
		{code: http.StatusForbidden, zh: "请求已被拒绝", en: "Request denied"},
		{code: http.StatusRequestEntityTooLarge, zh: "请求内容过大", en: "Request is too large"},
		{code: http.StatusTooManyRequests, zh: "请求过于频繁，请稍后重试", en: "Too many requests. Try again later."},
		{code: http.StatusInternalServerError, zh: "请求处理失败", en: "Request could not be completed"},
	}

	for _, test := range tests {
		if got := publicErrorMessage(test.code, "zh"); got != test.zh {
			t.Errorf("publicErrorMessage(%d, zh) = %q, want %q", test.code, got, test.zh)
		}
		if got := publicErrorMessage(test.code, "en"); got != test.en {
			t.Errorf("publicErrorMessage(%d, en) = %q, want %q", test.code, got, test.en)
		}
	}
}

func TestSessionStorePurgesExpiredTokensWhenAdding(t *testing.T) {
	now := time.Now()
	store := &sessionStore{tokens: map[string]sessionData{
		"expired": {ExpiresAt: now.Add(-time.Minute), CSRFToken: "expired-csrf"},
		"active":  {ExpiresAt: now.Add(time.Hour), CSRFToken: "active-csrf"},
	}}

	store.add("new", sessionData{ExpiresAt: now.Add(2 * time.Hour), CSRFToken: "new-csrf"})

	if store.valid("expired", now) {
		t.Error("expired token remains valid")
	}
	if !store.valid("active", now) || !store.valid("new", now) {
		t.Error("active tokens must remain valid")
	}
	if _, exists := store.tokens["expired"]; exists {
		t.Error("expired token was not purged")
	}
	if token, exists := store.csrf("active", now); !exists || token != "active-csrf" {
		t.Errorf("csrf(active) = (%q, %v), want active token", token, exists)
	}
	if _, exists := store.csrf("expired", now); exists {
		t.Error("expired session returned a CSRF token")
	}
}

func TestLoginAttemptStoreResetsAndStartsNewWindow(t *testing.T) {
	now := time.Now()
	store := &loginAttemptStore{attempts: map[string]loginAttempt{}}
	for attempt := 0; attempt < 5; attempt++ {
		if !store.reserve("client", now) {
			t.Fatalf("reserve attempt %d rejected unexpectedly", attempt+1)
		}
	}
	if store.reserve("client", now) {
		t.Error("sixth attempt in window was allowed")
	}
	if !store.reserve("client", now.Add(16*time.Minute)) {
		t.Error("attempt after rate-limit window was rejected")
	}
	store.reset("client")
	if !store.reserve("client", now.Add(16*time.Minute)) {
		t.Error("attempt after reset was rejected")
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "empty username", config: Config{AdminPassword: testPassword}},
		{name: "blank username", config: Config{AdminUsername: "  ", AdminPassword: testPassword}},
		{name: "empty password", config: Config{AdminUsername: testUsername}},
		{name: "negative CPU usage", config: Config{AdminUsername: testUsername, AdminPassword: testPassword, Metrics: DashboardMetrics{CPUUsage: -1}}},
		{name: "CPU usage over 100", config: Config{AdminUsername: testUsername, AdminPassword: testPassword, Metrics: DashboardMetrics{CPUUsage: 101}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, err := New(test.config)
			if err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
			if handler != nil {
				t.Error("New() returned a handler with a validation error")
			}
		})
	}
}

func TestModulePlaceholderRoutes(t *testing.T) {
	handler := newTestHandler(t)
	session := login(t, handler)
	tests := []struct {
		path  string
		label string
	}{
		{path: "/slurm/config", label: "Slurm 配置"},
		{path: "/slurm/users", label: "/slurm/users"},
		{path: "/slurm/core-hours", label: "/slurm/core-hours"},
		{path: "/system/files", label: "文件管理"},
		{path: "/terminal", label: "终端"},
		{path: "/platform/users", label: "平台用户"},
	}

	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.AddCookie(session)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			assertStatus(t, response, http.StatusOK)
			assertBodyContains(t, response, test.label)
			assertBodyContains(t, response, "等待系统集成")
		})
	}
}

func TestResponsesIncludeSecurityHeaders(t *testing.T) {
	handler := newTestHandler(t)

	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assertStatus(t, response, http.StatusOK)
	assertHeader(t, response, "Content-Security-Policy", "default-src 'self'; style-src 'self'; img-src 'self' data:; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
	assertHeader(t, response, "Referrer-Policy", "no-referrer")
	assertHeader(t, response, "X-Content-Type-Options", "nosniff")
	assertHeader(t, response, "X-Frame-Options", "DENY")
}

func TestSafeNextOnlyAllowsLocalAbsolutePaths(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "empty", value: "", want: "/dashboard"},
		{name: "absolute URL", value: "https://evil.example/steal", want: "/dashboard"},
		{name: "protocol relative URL", value: "//evil.example/steal", want: "/dashboard"},
		{name: "backslash authority", value: `/\evil.example/steal`, want: "/dashboard"},
		{name: "relative path", value: "slurm/jobs", want: "/dashboard"},
		{name: "local path", value: "/slurm/jobs?state=running", want: "/slurm/jobs?state=running"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeNext(test.value); got != test.want {
				t.Errorf("safeNext(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()

	handler, err := New(Config{
		AdminUsername: testUsername,
		AdminPassword: testPassword,
		Metrics: DashboardMetrics{
			OnlineNodes: 12,
			RunningJobs: 34,
			QueuedJobs:  5,
			CPUUsage:    67,
		},
		MetricsAvailable: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

func login(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	session, _ := loginWithCSRF(t, handler)
	return session
}

func loginWithCSRF(t *testing.T, handler http.Handler) (*http.Cookie, *http.Cookie) {
	t.Helper()

	response := postForm(handler, "/login", url.Values{
		"username": {testUsername},
		"password": {testPassword},
	}, nil)
	assertStatus(t, response, http.StatusSeeOther)
	return findCookie(t, response.Result().Cookies(), sessionCookie), findCookie(t, response.Result().Cookies(), "openhpc_csrf")
}

func postForm(handler http.Handler, path string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	return postFormWithCookies(handler, path, values, cookie)
}

func postProtectedForm(handler http.Handler, path string, values url.Values, session, csrf *http.Cookie) *httptest.ResponseRecorder {
	protectedValues := make(url.Values, len(values)+1)
	for key, entries := range values {
		protectedValues[key] = append([]string(nil), entries...)
	}
	protectedValues.Set("_csrf", csrf.Value)
	return postFormWithCookies(handler, path, protectedValues, session, csrf)
}

func postFormWithCookies(handler http.Handler, path string, values url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		if cookie != nil {
			request.AddCookie(cookie)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func newFailedLoginRequest(remoteAddress, forwardedFor string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{
		"username": {testUsername},
		"password": {"wrong"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	request.RemoteAddr = remoteAddress
	return request
}

type stubMetricsProvider struct {
	metrics cluster.Metrics
	err     error
	calls   int
}

func (p *stubMetricsProvider) Snapshot(_ context.Context) (cluster.Metrics, error) {
	p.calls++
	return p.metrics, p.err
}

func cleanupHandler(t *testing.T, handler http.Handler) {
	t.Helper()
	closer, ok := handler.(interface{ Close() error })
	if !ok {
		return
	}
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Errorf("handler Close() error = %v", err)
		}
	})
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response did not set %q cookie", name)
	return nil
}

func assertNoActiveSessionCookie(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookie && cookie.Value != "" {
			t.Errorf("response created active %q cookie", sessionCookie)
		}
	}
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", response.Code, want, response.Body.String())
	}
}

func assertHeader(t *testing.T, response *httptest.ResponseRecorder, name, want string) {
	t.Helper()
	if got := response.Header().Get(name); got != want {
		t.Errorf("header %s = %q, want %q", name, got, want)
	}
}

func assertBodyContains(t *testing.T, response *httptest.ResponseRecorder, expected string) {
	t.Helper()
	if !strings.Contains(response.Body.String(), expected) {
		t.Errorf("body does not contain %q; body: %s", expected, response.Body.String())
	}
}

func assertBodyNotContains(t *testing.T, response *httptest.ResponseRecorder, unexpected string) {
	t.Helper()
	if strings.Contains(response.Body.String(), unexpected) {
		t.Errorf("body unexpectedly contains %q; body: %s", unexpected, response.Body.String())
	}
}
