package web

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/platform"
)

func TestAuditPageRequiresAuthentication(t *testing.T) {
	handler := newAuditPageHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/audit", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
	assertHeader(t, response, "Location", "/login?next=%2Faudit")
}

func TestAuditPageShowsEventsInApplicationShell(t *testing.T) {
	handler := newAuditPageHandler(t)
	response := getAuthenticated(t, handler, "/audit", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		`class="app-shell"`, `aria-current="page"`, "审计日志", "操作者", "动作", "结果", "UTC 时间",
		"auth.login", testUsername, "success", `href="/audit"`,
	} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "等待系统集成")
}

func TestAuditPageSupportsEnglish(t *testing.T) {
	handler := newAuditPageHandler(t)
	response := getAuthenticated(t, handler, "/audit", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"Audit log", "Recent security and operations events", "Actor", "Action", "Outcome", "Time (UTC)"} {
		assertBodyContains(t, response, value)
	}
}

func TestAuditPageEscapesStoredValues(t *testing.T) {
	handler := newAuditPageHandler(t)
	store := handler.(*Handler).audit
	if err := store.Record(context.Background(), platform.AuditEvent{
		Actor: `<script>alert("actor")</script>`, Action: `job<&"`, Outcome: `unknown<&"`, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	response := getAuthenticated(t, handler, "/audit", "zh")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"&lt;script&gt;alert", "job&lt;&amp;&#34;", "unknown&lt;&amp;&#34;"} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, `<script>alert`)
}

func TestAuditPageUsesStableCursorPagination(t *testing.T) {
	handler := newAuditPageHandler(t)
	session := login(t, handler)
	store := handler.(*Handler).audit
	for index := 0; index < auditPageSize; index++ {
		if err := store.Record(context.Background(), platform.AuditEvent{
			Actor: "admin", Action: "test.event:" + strconv.Itoa(index), Outcome: "success", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/audit", nil)
	request.AddCookie(session)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	match := regexp.MustCompile(`href="/audit\?before_id=([1-9][0-9]*)"`).FindStringSubmatch(response.Body.String())
	if len(match) != 2 {
		t.Fatalf("audit page missing canonical cursor link: %s", response.Body.String())
	}
	assertBodyContains(t, response, "test.event:0")
	assertBodyNotContains(t, response, "auth.login")

	request = httptest.NewRequest(http.MethodGet, "/audit?before_id="+match[1], nil)
	request.AddCookie(session)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "auth.login")
	assertBodyNotContains(t, response, "test.event:0")
}

func TestAuditPageRejectsInvalidCursors(t *testing.T) {
	handler := newAuditPageHandler(t)
	session := login(t, handler)
	for _, cursor := range []string{"0", "-1", "01", "abc", "9223372036854775808"} {
		request := httptest.NewRequest(http.MethodGet, "/audit?before_id="+cursor, nil)
		request.AddCookie(session)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertStatus(t, response, http.StatusBadRequest)
		assertBodyContains(t, response, "请求参数无效")
	}
}

func TestAuditPageShowsEmptyCursorPage(t *testing.T) {
	handler := newAuditPageHandler(t)
	session := login(t, handler)
	request := httptest.NewRequest(http.MethodGet, "/audit?before_id=1", nil)
	request.AddCookie(session)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "暂无审计记录")
}

func TestAuditPageRedactsDatabaseErrors(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "secret-audit.db")
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, DatabasePath: databasePath})
	if err != nil {
		t.Fatal(err)
	}
	typedHandler := handler.(*Handler)
	session := login(t, handler)
	if err := typedHandler.audit.Close(); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/audit", nil)
	request.AddCookie(session)
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "审计日志暂不可用")
	assertBodyNotContains(t, response, databasePath)
	assertBodyNotContains(t, response, "sql: database is closed")
	if err := closeJobOutputRoots(typedHandler.jobOutputRoots); err != nil {
		t.Fatal(err)
	}
}

func TestAuditTemplateExecutesWithEmptyPage(t *testing.T) {
	templates, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	view := auditView{appChrome: appChrome{}, AuditAvailable: true, Labels: auditCopyFor("zh")}
	if err := templates.ExecuteTemplate(&output, "audit.html", view); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("audit template output is empty")
	}
}

func newAuditPageHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := New(Config{
		AdminUsername: testUsername, AdminPassword: testPassword,
		DatabasePath: filepath.Join(t.TempDir(), "audit.db"), MetricsAvailable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cleanupHandler(t, handler)
	return handler
}
