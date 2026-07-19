package ldapdirectory

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/acdiost/openhpc-web/internal/directory"
	ber "github.com/go-asn1-ber/asn1-ber"
	ldap "github.com/go-ldap/ldap/v3"
)

const (
	maxLDAPTimeout      = 30 * time.Second
	maxLDAPResults      = 500
	maxLDAPPacketBytes  = 8 << 20
	maxLDAPSearchBytes  = 4 << 20
	maxDirectoryField   = 1024
	maxDirectoryMembers = 500
	maxDirectoryQuery   = 128
)

func init() {
	ber.MaxPacketLengthBytes = maxLDAPPacketBytes
}

var (
	userAttributes  = []string{"uid", "cn", "mail", "uidNumber", "gidNumber", "homeDirectory", "loginShell"}
	groupAttributes = []string{"cn", "description", "gidNumber", "memberUid"}

	errLDAPSearchResponseTooLarge = errors.New("LDAP search response exceeds resource limit")
)

type Config struct {
	URL           string
	BaseDN        string
	UserBaseDN    string
	GroupBaseDN   string
	BindDN        string
	BindPassword  string
	CAFile        string
	Timeout       time.Duration
	MaxResults    int
	AllowInsecure bool
}

type ldapConnection interface {
	Bind(string, string) error
	SearchAsync(context.Context, *ldap.SearchRequest, int) ldap.Response
	SetTimeout(time.Duration)
	Close() error
}

type dialLDAP func(context.Context) (ldapConnection, error)

type Client struct {
	config Config
	dial   dialLDAP
}

func New(config Config) (*Client, error) {
	tlsConfig, err := validateConfig(config)
	if err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(config.URL)
	address := endpoint.Host
	if endpoint.Port() == "" {
		address = net.JoinHostPort(endpoint.Hostname(), "636")
	}
	netDialer := &net.Dialer{Timeout: config.Timeout}
	return &Client{config: normalizeConfig(config), dial: func(ctx context.Context) (ldapConnection, error) {
		var networkConnection net.Conn
		var err error
		if endpoint.Scheme == "ldaps" {
			networkConnection, err = (&tls.Dialer{NetDialer: netDialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
		} else {
			networkConnection, err = netDialer.DialContext(ctx, "tcp", address)
		}
		if err != nil {
			return nil, fmt.Errorf("connect LDAP directory: %w", err)
		}
		connection := ldap.NewConn(networkConnection, endpoint.Scheme == "ldaps")
		connection.Start()
		return connection, nil
	}}, nil
}

func ValidateConfig(config Config) error {
	_, err := validateConfig(config)
	return err
}

func ValidateCAFile(path string) error { return validateRootOwnedCAFile(path) }

func newClientWithDialer(config Config, dial dialLDAP) (*Client, error) {
	if dial == nil {
		return nil, errors.New("LDAP dialer is required")
	}
	if _, err := validateConfig(config); err != nil {
		return nil, err
	}
	return &Client{config: normalizeConfig(config), dial: dial}, nil
}

func normalizeConfig(config Config) Config {
	if config.UserBaseDN == "" {
		config.UserBaseDN = config.BaseDN
	}
	if config.GroupBaseDN == "" {
		config.GroupBaseDN = config.BaseDN
	}
	return config
}

func validateConfig(config Config) (*tls.Config, error) {
	endpoint, err := url.Parse(config.URL)
	if err != nil || (endpoint.Scheme != "ldaps" && !(endpoint.Scheme == "ldap" && config.AllowInsecure)) || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("LDAP URL must be an ldaps URL, or ldap when explicitly enabled, without credentials, query or fragment")
	}
	for name, value := range map[string]string{"base DN": config.BaseDN, "user base DN": config.UserBaseDN, "group base DN": config.GroupBaseDN, "bind DN": config.BindDN} {
		if value == "" && (name == "user base DN" || name == "group base DN" || name == "bind DN") {
			continue
		}
		if value == "" {
			return nil, errors.New("LDAP base DN is required")
		}
		if _, err := ldap.ParseDN(value); err != nil {
			return nil, fmt.Errorf("LDAP %s is invalid", name)
		}
	}
	if (config.BindDN == "") != (config.BindPassword == "") {
		return nil, errors.New("LDAP bind DN and password must be configured together")
	}
	if config.Timeout <= 0 || config.Timeout > maxLDAPTimeout {
		return nil, errors.New("LDAP timeout must be between zero and 30 seconds")
	}
	if config.MaxResults <= 0 || config.MaxResults > maxLDAPResults {
		return nil, errors.New("LDAP result limit must be between 1 and 500")
	}
	if endpoint.Scheme == "ldap" {
		return nil, nil
	}
	var roots *x509.CertPool
	if config.CAFile != "" {
		roots = x509.NewCertPool()
		if !filepath.IsAbs(config.CAFile) || filepath.Clean(config.CAFile) != config.CAFile {
			return nil, errors.New("LDAP CA file must be an absolute clean path")
		}
		if err := validateRootOwnedCAFile(config.CAFile); err != nil {
			return nil, errors.New("LDAP CA file must be a non-writable regular file")
		}
		certificate, err := os.ReadFile(config.CAFile)
		if err != nil || !roots.AppendCertsFromPEM(certificate) {
			return nil, errors.New("LDAP CA file does not contain a valid certificate")
		}
	} else {
		roots, err = x509.SystemCertPool()
		if err != nil || roots == nil {
			roots = x509.NewCertPool()
		}
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: endpoint.Hostname(), RootCAs: roots}, nil
}

func (c *Client) Search(ctx context.Context, query string) (directory.Page, error) {
	if err := validateQuery(query); err != nil {
		return directory.Page{}, err
	}
	connection, stopCancellation, err := c.open(ctx)
	if err != nil {
		return directory.Page{}, err
	}
	defer connection.Close()
	defer stopCancellation()
	escaped := ldap.EscapeFilter(query)
	userFilter := "(objectClass=posixAccount)"
	groupFilter := "(objectClass=posixGroup)"
	if query != "" {
		userFilter = "(&(objectClass=posixAccount)(|(uid=*" + escaped + "*)(cn=*" + escaped + "*)(mail=*" + escaped + "*)))"
		groupFilter = "(&(objectClass=posixGroup)(|(cn=*" + escaped + "*)(description=*" + escaped + "*)))"
	}
	userResult, userServerTruncated, err := boundedSearch(ctx, connection, c.searchRequest(c.config.UserBaseDN, userFilter, userAttributes, c.config.MaxResults+1))
	if err != nil {
		return directory.Page{}, fmt.Errorf("search LDAP users: %w", err)
	}
	groupResult, groupServerTruncated, err := boundedSearch(ctx, connection, c.searchRequest(c.config.GroupBaseDN, groupFilter, groupAttributes, c.config.MaxResults+1))
	if err != nil {
		return directory.Page{}, fmt.Errorf("search LDAP groups: %w", err)
	}
	users, userTruncated, err := parseUsers(userResult.Entries, c.config.MaxResults)
	if err != nil {
		return directory.Page{}, err
	}
	groups, groupTruncated, err := parseGroups(groupResult.Entries, c.config.MaxResults)
	if err != nil {
		return directory.Page{}, err
	}
	return directory.Page{Users: users, Groups: groups, Truncated: userServerTruncated || groupServerTruncated || userTruncated || groupTruncated}, nil
}

func (c *Client) User(ctx context.Context, uid string) (directory.User, bool, error) {
	if err := validateDirectoryName(uid); err != nil {
		return directory.User{}, false, err
	}
	connection, stopCancellation, err := c.open(ctx)
	if err != nil {
		return directory.User{}, false, err
	}
	defer connection.Close()
	defer stopCancellation()
	filter := "(&(objectClass=posixAccount)(uid=" + ldap.EscapeFilter(uid) + "))"
	result, truncated, err := boundedSearch(ctx, connection, c.searchRequest(c.config.UserBaseDN, filter, userAttributes, 2))
	if err != nil {
		return directory.User{}, false, fmt.Errorf("read LDAP user: %w", err)
	}
	if len(result.Entries) == 0 {
		return directory.User{}, false, nil
	}
	if truncated || len(result.Entries) != 1 {
		return directory.User{}, false, errors.New("LDAP user identifier is not unique")
	}
	user, err := parseUser(result.Entries[0])
	return user, err == nil, err
}

func (c *Client) Group(ctx context.Context, name string) (directory.Group, bool, error) {
	if err := validateDirectoryName(name); err != nil {
		return directory.Group{}, false, err
	}
	connection, stopCancellation, err := c.open(ctx)
	if err != nil {
		return directory.Group{}, false, err
	}
	defer connection.Close()
	defer stopCancellation()
	filter := "(&(objectClass=posixGroup)(cn=" + ldap.EscapeFilter(name) + "))"
	result, truncated, err := boundedSearch(ctx, connection, c.searchRequest(c.config.GroupBaseDN, filter, groupAttributes, 2))
	if err != nil {
		return directory.Group{}, false, fmt.Errorf("read LDAP group: %w", err)
	}
	if len(result.Entries) == 0 {
		return directory.Group{}, false, nil
	}
	if truncated || len(result.Entries) != 1 {
		return directory.Group{}, false, errors.New("LDAP group identifier is not unique")
	}
	group, err := parseGroup(result.Entries[0])
	return group, err == nil, err
}

func (c *Client) Authenticate(ctx context.Context, uid, password string) (bool, error) {
	if err := validateDirectoryName(uid); err != nil {
		return false, err
	}
	connection, stopCancellation, err := c.open(ctx)
	if err != nil {
		return false, err
	}
	defer connection.Close()
	defer stopCancellation()
	filter := "(&(objectClass=posixAccount)(uid=" + ldap.EscapeFilter(uid) + "))"
	result, truncated, err := boundedSearch(ctx, connection, c.searchRequest(c.config.UserBaseDN, filter, userAttributes, 2))
	if err != nil {
		return false, fmt.Errorf("authenticate LDAP user: %w", err)
	}
	if len(result.Entries) == 0 {
		return false, nil
	}
	if truncated || len(result.Entries) != 1 {
		return false, errors.New("LDAP user identifier is not unique")
	}
	if err := connection.Bind(result.Entries[0].DN, password); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return false, nil
		}
		return false, fmt.Errorf("authenticate LDAP user: %w", err)
	}
	return true, nil
}

func (c *Client) open(ctx context.Context) (ldapConnection, func(), error) {
	connection, err := c.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	connection.SetTimeout(c.config.Timeout)
	stopCancellation := closeOnContext(ctx, connection)
	if c.config.BindDN != "" {
		if err := connection.Bind(c.config.BindDN, c.config.BindPassword); err != nil {
			stopCancellation()
			_ = connection.Close()
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, fmt.Errorf("bind LDAP directory: %w", err)
		}
	}
	if ctx.Err() != nil {
		stopCancellation()
		_ = connection.Close()
		return nil, nil, ctx.Err()
	}
	return connection, stopCancellation, nil
}

func (c *Client) searchRequest(baseDN, filter string, attributes []string, sizeLimit int) *ldap.SearchRequest {
	timeLimit := int(c.config.Timeout / time.Second)
	if timeLimit < 1 {
		timeLimit = 1
	}
	request := ldap.NewSearchRequest(baseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, sizeLimit, timeLimit, false, filter, append([]string(nil), attributes...), nil)
	request.EnforceSizeLimit = true
	return request
}

func boundedSearch(ctx context.Context, connection ldapConnection, request *ldap.SearchRequest) (*ldap.SearchResult, bool, error) {
	searchContext, cancel := context.WithCancel(ctx)
	defer cancel()
	response := connection.SearchAsync(searchContext, request, 0)
	result := &ldap.SearchResult{}
	usedBytes := 0
	for response.Next() {
		entry := response.Entry()
		if entry == nil {
			continue
		}
		if request.SizeLimit > 0 && len(result.Entries) >= request.SizeLimit {
			return result, true, nil
		}
		entryBytes, withinLimit := ldapEntryBudget(entry, maxLDAPSearchBytes-usedBytes)
		if !withinLimit {
			return nil, false, errLDAPSearchResponseTooLarge
		}
		usedBytes += entryBytes
		result.Entries = append(result.Entries, entry)
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	err := response.Err()
	truncated := errors.Is(err, ldap.ErrSizeLimitExceeded) || ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded)
	if err != nil && !truncated {
		return nil, false, err
	}
	return result, truncated, nil
}

func ldapEntryBudget(entry *ldap.Entry, remaining int) (int, bool) {
	if entry == nil || remaining < 128+len(entry.DN) {
		return 0, false
	}
	used := 128 + len(entry.DN)
	for _, attribute := range entry.Attributes {
		if attribute == nil || remaining-used < 64+len(attribute.Name) {
			return 0, false
		}
		used += 64 + len(attribute.Name)
		for _, value := range attribute.Values {
			if remaining-used < 32+len(value) {
				return 0, false
			}
			used += 32 + len(value)
		}
		for _, value := range attribute.ByteValues {
			if remaining-used < len(value) {
				return 0, false
			}
			used += len(value)
		}
	}
	return used, true
}

func closeOnContext(ctx context.Context, connection ldapConnection) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func validateRootOwnedCAFile(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("LDAP CA path is not protected")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errors.New("LDAP CA path is not root-owned")
		}
		if current == path && !info.Mode().IsRegular() {
			return errors.New("LDAP CA file is not regular")
		}
		if current != path && !info.IsDir() {
			return errors.New("LDAP CA parent is not a directory")
		}
		if current == string(filepath.Separator) {
			return nil
		}
	}
}

func parseUsers(entries []*ldap.Entry, limit int) ([]directory.User, bool, error) {
	users := make([]directory.User, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		user, err := parseUser(entry)
		if err != nil {
			return nil, false, err
		}
		if seen[user.UID] {
			return nil, false, errors.New("LDAP user identifier is duplicated")
		}
		seen[user.UID] = true
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].UID < users[j].UID })
	truncated := len(users) > limit
	if truncated {
		users = users[:limit]
	}
	return users, truncated, nil
}

func parseUser(entry *ldap.Entry) (directory.User, error) {
	if entry == nil {
		return directory.User{}, errors.New("LDAP user entry is missing")
	}
	uid := entry.GetAttributeValue("uid")
	if err := validateDirectoryName(uid); err != nil {
		return directory.User{}, errors.New("LDAP user uid is invalid")
	}
	uidNumber, err := parseDirectoryNumber(entry.GetAttributeValue("uidNumber"))
	if err != nil {
		return directory.User{}, errors.New("LDAP user uidNumber is invalid")
	}
	gidNumber, err := parseDirectoryNumber(entry.GetAttributeValue("gidNumber"))
	if err != nil {
		return directory.User{}, errors.New("LDAP user gidNumber is invalid")
	}
	name := entry.GetAttributeValue("cn")
	if name == "" {
		name = uid
	}
	values := []string{name, entry.GetAttributeValue("mail"), entry.GetAttributeValue("homeDirectory"), entry.GetAttributeValue("loginShell")}
	for _, value := range values {
		if err := validateField(value); err != nil {
			return directory.User{}, errors.New("LDAP user field is invalid")
		}
	}
	return directory.User{
		UID: uid, Name: name, Email: values[1], UIDNumber: uidNumber, GIDNumber: gidNumber,
		HomeDirectory: values[2], LoginShell: values[3],
	}, nil
}

func parseGroups(entries []*ldap.Entry, limit int) ([]directory.Group, bool, error) {
	groups := make([]directory.Group, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		group, err := parseGroup(entry)
		if err != nil {
			return nil, false, err
		}
		if seen[group.Name] {
			return nil, false, errors.New("LDAP group identifier is duplicated")
		}
		seen[group.Name] = true
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	truncated := len(groups) > limit
	if truncated {
		groups = groups[:limit]
	}
	return groups, truncated, nil
}

func parseGroup(entry *ldap.Entry) (directory.Group, error) {
	if entry == nil {
		return directory.Group{}, errors.New("LDAP group entry is missing")
	}
	name := entry.GetAttributeValue("cn")
	if err := validateDirectoryName(name); err != nil {
		return directory.Group{}, errors.New("LDAP group cn is invalid")
	}
	gidNumber, err := parseDirectoryNumber(entry.GetAttributeValue("gidNumber"))
	if err != nil {
		return directory.Group{}, errors.New("LDAP group gidNumber is invalid")
	}
	description := entry.GetAttributeValue("description")
	if err := validateField(description); err != nil {
		return directory.Group{}, errors.New("LDAP group description is invalid")
	}
	memberValues := entry.GetAttributeValues("memberUid")
	truncated := len(memberValues) > maxDirectoryMembers
	if len(memberValues) > maxDirectoryMembers+1 {
		memberValues = memberValues[:maxDirectoryMembers+1]
	}
	members := append([]string(nil), memberValues...)
	for _, member := range members {
		if err := validateDirectoryName(member); err != nil {
			return directory.Group{}, errors.New("LDAP group memberUid is invalid")
		}
	}
	sort.Strings(members)
	if len(members) > maxDirectoryMembers {
		members = members[:maxDirectoryMembers]
	}
	return directory.Group{Name: name, Description: description, GIDNumber: gidNumber, Members: members, MembersTruncated: truncated}, nil
}

func parseDirectoryNumber(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("number is required")
	}
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number < 0 {
		return 0, errors.New("number is invalid")
	}
	return number, nil
}

func validateDirectoryName(value string) error {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) {
		return errors.New("directory identifier is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("directory identifier is invalid")
		}
	}
	return nil
}

func validateQuery(value string) error {
	if len(value) > maxDirectoryQuery {
		return errors.New("LDAP query is too long")
	}
	return validateField(value)
}

func validateField(value string) error {
	if len(value) > maxDirectoryField || !utf8.ValidString(value) {
		return errors.New("directory field is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("directory field contains control characters")
		}
	}
	return nil
}
