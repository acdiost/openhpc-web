package ldapdirectory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	ldap "github.com/go-ldap/ldap/v3"
	"github.com/acdiost/openhpc-web/internal/directory"
)

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []Config{
		{},
		{URL: "ldap://ldap.example.com:389", BaseDN: "dc=example,dc=com", Timeout: time.Second, MaxResults: 10},
		{URL: "ldaps://user:password@ldap.example.com:636", BaseDN: "dc=example,dc=com", Timeout: time.Second, MaxResults: 10},
		{URL: "ldaps://ldap.example.com:636?secret=true", BaseDN: "dc=example,dc=com", Timeout: time.Second, MaxResults: 10},
		{URL: "ldaps://ldap.example.com:636", BaseDN: "not a dn", Timeout: time.Second, MaxResults: 10},
		{URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", BindDN: "cn=reader,dc=example,dc=com", Timeout: time.Second, MaxResults: 10},
		{URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", BindPassword: "secret", Timeout: time.Second, MaxResults: 10},
		{URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", Timeout: 0, MaxResults: 10},
		{URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", Timeout: time.Second, MaxResults: 0},
		{URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", Timeout: time.Second, MaxResults: 501},
	}
	for index, config := range tests {
		if client, err := New(config); err == nil || client != nil {
			t.Errorf("case %d New() = (%#v, %v), want validation error", index, client, err)
		}
	}
}

func TestNewAcceptsSecureConfigurationWithoutConnecting(t *testing.T) {
	config := Config{URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", Timeout: time.Second, MaxResults: 20}
	client, err := New(config)
	if err != nil || client == nil {
		t.Fatalf("New() = (%#v, %v)", client, err)
	}
	if err := ValidateConfig(config); err != nil {
		t.Fatalf("ValidateConfig() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Search(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("Search(cancelled) error = %v", err)
	}
}

func TestLDAPPacketSizeIsBounded(t *testing.T) {
	if ber.MaxPacketLengthBytes != maxLDAPPacketBytes {
		t.Fatalf("BER packet limit = %d, want %d", ber.MaxPacketLengthBytes, maxLDAPPacketBytes)
	}
	oversizedSequenceHeader := []byte{0x30, 0x84, 0x00, 0x80, 0x00, 0x01}
	if _, err := ber.ReadPacket(bytes.NewReader(oversizedSequenceHeader)); err == nil {
		t.Fatal("BER decoder accepted a packet larger than the LDAP packet limit")
	}
}

func TestNewRejectsUnprotectedCAFile(t *testing.T) {
	config := Config{
		URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", CAFile: filepath.Join(t.TempDir(), "ldap-ca.pem"),
		Timeout: time.Second, MaxResults: 20,
	}
	if err := os.WriteFile(config.CAFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config); err == nil {
		t.Fatal("New() error = nil for non-root-owned CA path")
	}
}

func TestClientSearchEscapesFilterAndUsesBoundedRequests(t *testing.T) {
	connection := &fakeLDAPConnection{results: []*ldap.SearchResult{
		{Entries: []*ldap.Entry{
			ldap.NewEntry("uid=bob,ou=People,dc=example,dc=com", map[string][]string{"uid": {"bob"}, "cn": {"Bob"}, "uidNumber": {"1002"}, "gidNumber": {"2000"}, "homeDirectory": {"/home/bob"}, "loginShell": {"/bin/bash"}}),
			ldap.NewEntry("uid=alice,ou=People,dc=example,dc=com", map[string][]string{"uid": {"alice"}, "cn": {"Alice"}, "mail": {"alice@example.com"}, "uidNumber": {"1001"}, "gidNumber": {"2000"}, "homeDirectory": {"/home/alice"}, "loginShell": {"/bin/zsh"}}),
		}},
		{Entries: []*ldap.Entry{
			ldap.NewEntry("cn=research,ou=Group,dc=example,dc=com", map[string][]string{"cn": {"research"}, "description": {"Research users"}, "gidNumber": {"2000"}, "memberUid": {"bob", "alice"}}),
		}},
	}}
	client := newTestClient(t, connection, 2)
	page, err := client.Search(context.Background(), `*)(uid=*)`)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{page.Users[0].UID, page.Users[1].UID}; !reflect.DeepEqual(got, []string{"alice", "bob"}) {
		t.Errorf("users = %#v", got)
	}
	if len(page.Groups) != 1 || !reflect.DeepEqual(page.Groups[0].Members, []string{"alice", "bob"}) {
		t.Errorf("groups = %#v", page.Groups)
	}
	if len(connection.requests) != 2 {
		t.Fatalf("requests = %d", len(connection.requests))
	}
	escaped := ldap.EscapeFilter(`*)(uid=*)`)
	for _, request := range connection.requests {
		if !strings.Contains(request.Filter, escaped) || strings.Contains(request.Filter, `*)(uid=*)`) {
			t.Errorf("unsafe filter = %q", request.Filter)
		}
		if request.Scope != ldap.ScopeWholeSubtree || request.DerefAliases != ldap.NeverDerefAliases || request.SizeLimit != 3 || request.TimeLimit != 1 {
			t.Errorf("request bounds = %#v", request)
		}
		if !request.EnforceSizeLimit {
			t.Error("client size limit is not enforced")
		}
		if containsString(request.Attributes, "userPassword") {
			t.Errorf("sensitive attributes requested: %#v", request.Attributes)
		}
	}
	if connection.bindUsername != "cn=reader,dc=example,dc=com" || connection.bindPassword != "test-password" {
		t.Errorf("bind = %q / %q", connection.bindUsername, connection.bindPassword)
	}
}

func TestClientSearchMarksTruncatedResults(t *testing.T) {
	connection := &fakeLDAPConnection{results: []*ldap.SearchResult{
		{Entries: []*ldap.Entry{
			userEntry("charlie", "1003"), userEntry("alice", "1001"), userEntry("bob", "1002"),
		}},
		{Entries: []*ldap.Entry{}},
	}}
	page, err := newTestClient(t, connection, 2).Search(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Users) != 2 || page.Users[0].UID != "alice" || page.Users[1].UID != "bob" {
		t.Errorf("page = %#v", page)
	}
}

func TestClientUserAndGroupDetailsUseExactEscapedIdentifiers(t *testing.T) {
	connection := &fakeLDAPConnection{results: []*ldap.SearchResult{
		{Entries: []*ldap.Entry{userEntry("alice", "1001")}},
		{Entries: []*ldap.Entry{ldap.NewEntry("cn=research,dc=example,dc=com", map[string][]string{"cn": {"research"}, "gidNumber": {"2000"}, "memberUid": {"alice"}})}},
	}}
	client := newTestClient(t, connection, 10)
	user, found, err := client.User(context.Background(), "alice")
	if err != nil || !found || user.UID != "alice" {
		t.Fatalf("User() = (%#v, %v, %v)", user, found, err)
	}
	group, found, err := client.Group(context.Background(), "research")
	if err != nil || !found || group.Name != "research" {
		t.Fatalf("Group() = (%#v, %v, %v)", group, found, err)
	}
	if connection.requests[0].Filter != "(&(objectClass=posixAccount)(uid=alice))" {
		t.Errorf("user filter = %q", connection.requests[0].Filter)
	}
	if connection.requests[1].Filter != "(&(objectClass=posixGroup)(cn=research))" {
		t.Errorf("group filter = %q", connection.requests[1].Filter)
	}
}

func TestClientDetailsHandleMissingAndDuplicateEntries(t *testing.T) {
	connection := &fakeLDAPConnection{results: []*ldap.SearchResult{
		{},
		{Entries: []*ldap.Entry{userEntry("alice", "1001"), userEntry("alice", "1001")}},
		{},
		{Entries: []*ldap.Entry{
			ldap.NewEntry("cn=research,dc=example,dc=com", map[string][]string{"cn": {"research"}, "gidNumber": {"2000"}}),
			ldap.NewEntry("cn=research,dc=example,dc=com", map[string][]string{"cn": {"research"}, "gidNumber": {"2000"}}),
		}},
	}}
	client := newTestClient(t, connection, 10)
	if _, found, err := client.User(context.Background(), "missing"); err != nil || found {
		t.Errorf("User(missing) = (%v, %v)", found, err)
	}
	if _, _, err := client.User(context.Background(), "alice"); err == nil {
		t.Error("User(duplicate) error = nil")
	}
	if _, found, err := client.Group(context.Background(), "missing"); err != nil || found {
		t.Errorf("Group(missing) = (%v, %v)", found, err)
	}
	if _, _, err := client.Group(context.Background(), "research"); err == nil {
		t.Error("Group(duplicate) error = nil")
	}
}

func TestClientAcceptsBoundedSizeLimitResult(t *testing.T) {
	connection := &fakeLDAPConnection{
		results:      []*ldap.SearchResult{{Entries: []*ldap.Entry{userEntry("alice", "1001")}}, {}},
		searchErrors: []error{ldap.ErrSizeLimitExceeded, nil},
	}
	page, err := newTestClient(t, connection, 10).Search(context.Background(), "")
	if err != nil || len(page.Users) != 1 || !page.Truncated {
		t.Fatalf("Search() = (%#v, %v)", page, err)
	}
}

func TestBoundedSearchRejectsCumulativeResponseOverBudget(t *testing.T) {
	payload := strings.Repeat("x", maxLDAPSearchBytes/3)
	connection := &fakeLDAPConnection{results: []*ldap.SearchResult{{Entries: []*ldap.Entry{
		ldap.NewEntry("uid=one,dc=example,dc=com", map[string][]string{"mail": {payload}}),
		ldap.NewEntry("uid=two,dc=example,dc=com", map[string][]string{"mail": {payload}}),
	}}}}
	request := ldap.NewSearchRequest("dc=example,dc=com", ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 10, 1, false, "(objectClass=*)", userAttributes, nil)
	if _, _, err := boundedSearch(context.Background(), connection, request); !errors.Is(err, errLDAPSearchResponseTooLarge) {
		t.Fatalf("boundedSearch() error = %v", err)
	}
}

func TestParseGroupLimitsAndCopiesMembers(t *testing.T) {
	members := make([]string, maxDirectoryMembers+1)
	for index := range members {
		members[index] = fmt.Sprintf("user-%03d", index)
	}
	entry := ldap.NewEntry("cn=large,dc=example,dc=com", map[string][]string{"cn": {"large"}, "gidNumber": {"2000"}, "memberUid": members})
	group, err := parseGroup(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !group.MembersTruncated || len(group.Members) != maxDirectoryMembers {
		t.Errorf("group = %#v", group)
	}
	members[0] = "changed"
	if group.Members[0] == "changed" {
		t.Error("group members alias LDAP entry values")
	}
}

func TestParseGroupDoesNotProcessMembersPastDisplayLimit(t *testing.T) {
	members := make([]string, maxDirectoryMembers+100_000)
	for index := range members {
		members[index] = fmt.Sprintf("user-%06d", index)
	}
	members[len(members)-1] = "invalid\nmember"
	entry := ldap.NewEntry("cn=large,dc=example,dc=com", map[string][]string{
		"cn": {"large"}, "gidNumber": {"2000"}, "memberUid": members,
	})
	group, err := parseGroup(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !group.MembersTruncated || len(group.Members) != maxDirectoryMembers {
		t.Errorf("group members = %d, truncated = %v", len(group.Members), group.MembersTruncated)
	}
}

func TestParseEntriesAcceptUnicodeDirectoryNames(t *testing.T) {
	user, err := parseUser(ldap.NewEntry("uid=用户 甲,dc=example,dc=com", map[string][]string{
		"uid": {"用户 甲"}, "cn": {"用户甲"}, "uidNumber": {"1001"}, "gidNumber": {"2000"},
	}))
	if err != nil || user.UID != "用户 甲" {
		t.Fatalf("parseUser() = (%#v, %v)", user, err)
	}
	group, err := parseGroup(ldap.NewEntry("cn=研发/一组,dc=example,dc=com", map[string][]string{
		"cn": {"研发/一组"}, "gidNumber": {"2000"}, "memberUid": {"用户 甲"},
	}))
	if err != nil || group.Name != "研发/一组" {
		t.Fatalf("parseGroup() = (%#v, %v)", group, err)
	}
}

func TestClientCancellationClosesActiveConnection(t *testing.T) {
	connection := newCancellableConnection()
	client, err := newClientWithDialer(Config{
		URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com", Timeout: 10 * time.Second, MaxResults: 10,
	}, func(context.Context) (ldapConnection, error) { return connection, nil })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.Search(ctx, ""); done <- err }()
	<-connection.started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Search(cancelled) error = nil")
		}
	case <-time.After(time.Second):
		t.Fatal("Search did not stop after context cancellation")
	}
}

func TestClientCancellationClosesConnectionDuringBind(t *testing.T) {
	connection := newBindBlockingConnection()
	client := newTestClientWithConnection(t, connection, 10)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.Search(ctx, ""); done <- err }()
	<-connection.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Search(cancelled during bind) error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Search did not stop after cancellation during bind")
	}
}

func TestClientRejectsMalformedDirectoryEntries(t *testing.T) {
	entries := []*ldap.Entry{
		ldap.NewEntry("uid=missing,dc=example,dc=com", map[string][]string{"cn": {"Missing UID"}, "uidNumber": {"1001"}, "gidNumber": {"2000"}}),
		ldap.NewEntry("uid=bad-number,dc=example,dc=com", map[string][]string{"uid": {"bad-number"}, "uidNumber": {"not-a-number"}, "gidNumber": {"2000"}}),
		ldap.NewEntry("uid=control,dc=example,dc=com", map[string][]string{"uid": {"control"}, "cn": {"bad\nname"}, "uidNumber": {"1001"}, "gidNumber": {"2000"}}),
	}
	for _, entry := range entries {
		connection := &fakeLDAPConnection{results: []*ldap.SearchResult{{Entries: []*ldap.Entry{entry}}, {}}}
		if _, err := newTestClient(t, connection, 10).Search(context.Background(), ""); err == nil {
			t.Errorf("entry %#v accepted", entry)
		}
	}
}

func TestClientRejectsInvalidQueriesBeforeDial(t *testing.T) {
	client := newTestClient(t, &fakeLDAPConnection{}, 10)
	for _, query := range []string{strings.Repeat("x", maxDirectoryQuery+1), "bad\nquery"} {
		if _, err := client.Search(context.Background(), query); err == nil {
			t.Errorf("Search(%q) error = nil", query)
		}
	}
}

func TestParseGroupsRejectsDuplicateAndInvalidMembers(t *testing.T) {
	duplicate := ldap.NewEntry("cn=research,dc=example,dc=com", map[string][]string{"cn": {"research"}, "gidNumber": {"2000"}})
	if _, _, err := parseGroups([]*ldap.Entry{duplicate, duplicate}, 10); err == nil {
		t.Error("parseGroups(duplicate) error = nil")
	}
	invalidMember := ldap.NewEntry("cn=research,dc=example,dc=com", map[string][]string{"cn": {"research"}, "gidNumber": {"2000"}, "memberUid": {"not\nvalid"}})
	if _, err := parseGroup(invalidMember); err == nil {
		t.Error("parseGroup(invalid member) error = nil")
	}
}

func TestClientClosesConnectionAndWrapsErrors(t *testing.T) {
	sentinel := errors.New("bind password=secret")
	connection := &fakeLDAPConnection{bindErr: sentinel}
	_, err := newTestClient(t, connection, 10).Search(context.Background(), "alice")
	if !errors.Is(err, sentinel) || !connection.closed {
		t.Errorf("Search() error/close = %v / %v", err, connection.closed)
	}
}

func newTestClient(t *testing.T, connection *fakeLDAPConnection, maxResults int) *Client {
	return newTestClientWithConnection(t, connection, maxResults)
}

func newTestClientWithConnection(t *testing.T, connection ldapConnection, maxResults int) *Client {
	t.Helper()
	config := Config{
		URL: "ldaps://ldap.example.com:636", BaseDN: "dc=example,dc=com",
		BindDN: "cn=reader,dc=example,dc=com", BindPassword: "test-password",
		Timeout: time.Second, MaxResults: maxResults,
	}
	client, err := newClientWithDialer(config, func(context.Context) (ldapConnection, error) { return connection, nil })
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func userEntry(uid, uidNumber string) *ldap.Entry {
	return ldap.NewEntry("uid="+uid+",dc=example,dc=com", map[string][]string{
		"uid": {uid}, "cn": {strings.ToUpper(uid)}, "uidNumber": {uidNumber}, "gidNumber": {"2000"},
		"homeDirectory": {"/home/" + uid}, "loginShell": {"/bin/bash"},
	})
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type fakeLDAPConnection struct {
	requests     []*ldap.SearchRequest
	results      []*ldap.SearchResult
	searchErr    error
	searchErrors []error
	bindErr      error
	bindUsername string
	bindPassword string
	closed       bool
}

func (c *fakeLDAPConnection) Bind(username, password string) error {
	c.bindUsername, c.bindPassword = username, password
	return c.bindErr
}

func (c *fakeLDAPConnection) SearchAsync(_ context.Context, request *ldap.SearchRequest, bufferSize int) ldap.Response {
	if bufferSize != 0 {
		panic("LDAP searches must be unbuffered")
	}
	c.requests = append(c.requests, request)
	var result *ldap.SearchResult
	if len(c.results) == 0 {
		result = &ldap.SearchResult{}
	} else {
		result = c.results[0]
		c.results = c.results[1:]
	}
	if len(c.searchErrors) > 0 {
		err := c.searchErrors[0]
		c.searchErrors = c.searchErrors[1:]
		return &fakeLDAPResponse{entries: result.Entries, err: err}
	}
	return &fakeLDAPResponse{entries: result.Entries, err: c.searchErr}
}

func (c *fakeLDAPConnection) SetTimeout(time.Duration) {}
func (c *fakeLDAPConnection) Close() error             { c.closed = true; return nil }

var _ directory.Provider = (*Client)(nil)

type cancellableConnection struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

type bindBlockingConnection struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBindBlockingConnection() *bindBlockingConnection {
	return &bindBlockingConnection{started: make(chan struct{}), closed: make(chan struct{})}
}

func (c *bindBlockingConnection) Bind(string, string) error {
	c.once.Do(func() { close(c.started) })
	<-c.closed
	return context.Canceled
}
func (c *bindBlockingConnection) SearchAsync(context.Context, *ldap.SearchRequest, int) ldap.Response {
	return &fakeLDAPResponse{}
}
func (c *bindBlockingConnection) SetTimeout(time.Duration) {}
func (c *bindBlockingConnection) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func newCancellableConnection() *cancellableConnection {
	return &cancellableConnection{started: make(chan struct{}), closed: make(chan struct{})}
}

func (c *cancellableConnection) Bind(string, string) error { return nil }
func (c *cancellableConnection) SetTimeout(time.Duration)  {}
func (c *cancellableConnection) SearchAsync(context.Context, *ldap.SearchRequest, int) ldap.Response {
	c.once.Do(func() { close(c.started) })
	return &blockingLDAPResponse{closed: c.closed}
}
func (c *cancellableConnection) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

type fakeLDAPResponse struct {
	entries []*ldap.Entry
	index   int
	entry   *ldap.Entry
	err     error
}

func (r *fakeLDAPResponse) Next() bool {
	if r.index >= len(r.entries) {
		return false
	}
	r.entry = r.entries[r.index]
	r.index++
	return true
}
func (r *fakeLDAPResponse) Entry() *ldap.Entry       { return r.entry }
func (r *fakeLDAPResponse) Referral() string         { return "" }
func (r *fakeLDAPResponse) Controls() []ldap.Control { return nil }
func (r *fakeLDAPResponse) Err() error               { return r.err }

type blockingLDAPResponse struct {
	closed <-chan struct{}
}

func (r *blockingLDAPResponse) Next() bool {
	<-r.closed
	return false
}
func (r *blockingLDAPResponse) Entry() *ldap.Entry       { return nil }
func (r *blockingLDAPResponse) Referral() string         { return "" }
func (r *blockingLDAPResponse) Controls() []ldap.Control { return nil }
func (r *blockingLDAPResponse) Err() error               { return context.Canceled }
