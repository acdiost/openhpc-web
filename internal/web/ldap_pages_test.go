package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openhpc-web/openhpc-web/internal/directory"
)

func TestLDAPPageShowsEscapedUsersGroupsAndSearch(t *testing.T) {
	provider := &stubDirectoryProvider{page: directory.Page{
		Users:  []directory.User{{UID: "alice", Name: "<Alice>", Email: "alice&lab@example.com", UIDNumber: 1001, GIDNumber: 2000, HomeDirectory: "/home/alice", LoginShell: "/bin/bash"}},
		Groups: []directory.Group{{Name: "research", Description: `<script>alert(1)</script>`, GIDNumber: 2000, Members: []string{"alice"}, MembersTruncated: true}},
	}}
	handler := newDirectoryHandler(t, provider)
	session, csrf := loginWithCSRF(t, handler)
	response := postProtectedForm(handler, "/ldap/search", url.Values{"q": {" alice "}}, session, csrf)
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{
		"LDAP 目录", `name="q" value="alice"`, "目录用户", "目录组", "alice", "&lt;Alice&gt;", "alice&amp;lab@example.com",
		`href="/ldap/users/YWxpY2U"`, "research", "&lt;script&gt;alert(1)&lt;/script&gt;", `href="/ldap/groups/cmVzZWFyY2g"`, "<td>1+</td>",
	} {
		assertBodyContains(t, response, value)
	}
	assertBodyNotContains(t, response, "<script>alert(1)</script>")
	if provider.searchCalls != 1 || provider.lastQuery != "alice" {
		t.Errorf("search calls/query = %d / %q", provider.searchCalls, provider.lastQuery)
	}
	auditResponse := getAuthenticated(t, handler, "/audit", "en")
	assertBodyContains(t, auditResponse, "ldap.search")
}

func TestLDAPUnicodeNamesRoundTripThroughOpaqueKeys(t *testing.T) {
	provider := &stubDirectoryProvider{page: directory.Page{
		Users:  []directory.User{{UID: "用户 甲", Name: "用户甲", UIDNumber: 1001, GIDNumber: 2000}},
		Groups: []directory.Group{{Name: "研发/一组", GIDNumber: 2000}},
	}}
	handler := newDirectoryHandler(t, provider)
	response := getAuthenticated(t, handler, "/ldap", "zh")
	assertStatus(t, response, http.StatusOK)
	userKey, groupKey := encodeDirectoryKey("用户 甲"), encodeDirectoryKey("研发/一组")
	assertBodyContains(t, response, `href="/ldap/users/`+userKey+`"`)
	assertBodyContains(t, response, `href="/ldap/groups/`+groupKey+`"`)
	_ = getAuthenticated(t, handler, "/ldap/users/"+userKey, "zh")
	_ = getAuthenticated(t, handler, "/ldap/groups/"+groupKey, "zh")
	if provider.lastUID != "用户 甲" || provider.lastGroup != "研发/一组" {
		t.Errorf("detail identifiers = %q / %q", provider.lastUID, provider.lastGroup)
	}
}

func TestLDAPPageShowsTruncatedAndEmptyStates(t *testing.T) {
	provider := &stubDirectoryProvider{page: directory.Page{Truncated: true}}
	response := getAuthenticated(t, newDirectoryHandler(t, provider), "/ldap", "en")
	assertStatus(t, response, http.StatusOK)
	for _, value := range []string{"LDAP directory", "No directory users", "No directory groups", "Results were limited"} {
		assertBodyContains(t, response, value)
	}
}

func TestLDAPDetailsShowEscapedData(t *testing.T) {
	provider := &stubDirectoryProvider{
		user: directory.User{UID: "alice", Name: "Alice <Admin>", Email: "alice@example.com", UIDNumber: 1001, GIDNumber: 2000, HomeDirectory: "/home/alice", LoginShell: "/bin/zsh"}, userFound: true,
		group: directory.Group{Name: "research", Description: "R&D", GIDNumber: 2000, Members: []string{"alice", "bob"}, MembersTruncated: true}, groupFound: true,
	}
	handler := newDirectoryHandler(t, provider)
	userResponse := getAuthenticated(t, handler, "/ldap/users/YWxpY2U", "zh")
	assertStatus(t, userResponse, http.StatusOK)
	for _, value := range []string{"用户详情", "Alice &lt;Admin&gt;", "1001", "/home/alice", "/bin/zsh"} {
		assertBodyContains(t, userResponse, value)
	}
	groupResponse := getAuthenticated(t, handler, "/ldap/groups/cmVzZWFyY2g", "en")
	assertStatus(t, groupResponse, http.StatusOK)
	for _, value := range []string{"Group details", "research", "R&amp;D", "alice", "bob", "Member list was limited"} {
		assertBodyContains(t, groupResponse, value)
	}
}

func TestLDAPPageRejectsInvalidInputBeforeProvider(t *testing.T) {
	provider := &stubDirectoryProvider{}
	handler := newDirectoryHandler(t, provider)
	session, csrf := loginWithCSRF(t, handler)
	for _, query := range []string{strings.Repeat("x", 65), "\x00"} {
		response := postProtectedForm(handler, "/ldap/search", url.Values{"q": {query}}, session, csrf)
		assertStatus(t, response, http.StatusBadRequest)
	}
	for _, path := range []string{
		"/ldap?q=alice",
		"/ldap/users/not*base64",
		"/ldap/groups/not*base64",
	} {
		response := getAuthenticated(t, handler, path, "zh")
		assertStatus(t, response, http.StatusBadRequest)
	}
	if provider.searchCalls != 0 || provider.userCalls != 0 || provider.groupCalls != 0 {
		t.Errorf("provider calls = search:%d user:%d group:%d", provider.searchCalls, provider.userCalls, provider.groupCalls)
	}
}

func TestLDAPDetailsReturnNotFound(t *testing.T) {
	handler := newDirectoryHandler(t, &stubDirectoryProvider{})
	assertStatus(t, getAuthenticated(t, handler, "/ldap/users/bWlzc2luZw", "en"), http.StatusNotFound)
	assertStatus(t, getAuthenticated(t, handler, "/ldap/groups/bWlzc2luZw", "en"), http.StatusNotFound)
}

func TestLDAPPagesDegradeWithoutLeakingProviderErrors(t *testing.T) {
	provider := &stubDirectoryProvider{err: errors.New("bind cn=secret password=hunter2 /etc/ldap/private.pem")}
	handler := newDirectoryHandler(t, provider)
	for _, path := range []string{"/ldap", "/ldap/users/YWxpY2U", "/ldap/groups/cmVzZWFyY2g"} {
		response := getAuthenticated(t, handler, path, "zh")
		assertStatus(t, response, http.StatusOK)
		assertBodyContains(t, response, "LDAP 目录暂不可用")
		for _, secret := range []string{"cn=secret", "hunter2", "/etc/ldap/private.pem"} {
			assertBodyNotContains(t, response, secret)
		}
	}
}

func TestLDAPPageWithoutProviderIsUnavailable(t *testing.T) {
	response := getAuthenticated(t, newDirectoryHandler(t, nil), "/ldap", "en")
	assertStatus(t, response, http.StatusOK)
	assertBodyContains(t, response, "LDAP directory is temporarily unavailable")
	assertBodyNotContains(t, response, "Integration pending")
}

func TestLDAPPageRequiresAuthentication(t *testing.T) {
	handler := newDirectoryHandler(t, &stubDirectoryProvider{})
	request := httptest.NewRequest(http.MethodGet, "/ldap", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertStatus(t, response, http.StatusFound)
}

func TestLDAPPageLimitsConcurrentDirectoryReads(t *testing.T) {
	started := make(chan struct{}, maxConcurrentDirectoryReads)
	release := make(chan struct{})
	provider := &blockingDirectoryProvider{started: started, release: release}
	handler := newDirectoryHandler(t, provider)
	cookie := login(t, handler)
	responses := make(chan int, maxConcurrentDirectoryReads)
	for range maxConcurrentDirectoryReads {
		go func() {
			request := httptest.NewRequest(http.MethodGet, "/ldap", nil)
			request.AddCookie(cookie)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			responses <- response.Code
		}()
	}
	for range maxConcurrentDirectoryReads {
		<-started
	}
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	overflow := httptest.NewRequest(http.MethodGet, "/ldap", nil)
	overflow.AddCookie(cookie)
	overflowResponse := httptest.NewRecorder()
	handler.ServeHTTP(overflowResponse, overflow)
	assertStatus(t, overflowResponse, http.StatusTooManyRequests)
	close(release)
	released = true
	for range maxConcurrentDirectoryReads {
		if status := <-responses; status != http.StatusOK {
			t.Errorf("directory response status = %d", status)
		}
	}
}

func newDirectoryHandler(t *testing.T, provider directory.Provider) http.Handler {
	t.Helper()
	handler, err := New(Config{AdminUsername: testUsername, AdminPassword: testPassword, DirectoryProvider: provider})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cleanupHandler(t, handler)
	return handler
}

type stubDirectoryProvider struct {
	page        directory.Page
	user        directory.User
	group       directory.Group
	userFound   bool
	groupFound  bool
	err         error
	searchCalls int
	userCalls   int
	groupCalls  int
	lastQuery   string
	lastUID     string
	lastGroup   string
}

func (p *stubDirectoryProvider) Search(_ context.Context, query string) (directory.Page, error) {
	p.searchCalls++
	p.lastQuery = query
	return p.page, p.err
}

func (p *stubDirectoryProvider) User(_ context.Context, uid string) (directory.User, bool, error) {
	p.userCalls++
	p.lastUID = uid
	return p.user, p.userFound, p.err
}

func (p *stubDirectoryProvider) Group(_ context.Context, name string) (directory.Group, bool, error) {
	p.groupCalls++
	p.lastGroup = name
	return p.group, p.groupFound, p.err
}

type blockingDirectoryProvider struct {
	started chan<- struct{}
	release <-chan struct{}
}

func (p *blockingDirectoryProvider) Search(context.Context, string) (directory.Page, error) {
	p.started <- struct{}{}
	<-p.release
	return directory.Page{}, nil
}

func (p *blockingDirectoryProvider) User(context.Context, string) (directory.User, bool, error) {
	return directory.User{}, false, nil
}

func (p *blockingDirectoryProvider) Group(context.Context, string) (directory.Group, bool, error) {
	return directory.Group{}, false, nil
}
