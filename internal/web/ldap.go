package web

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/openhpc-web/openhpc-web/internal/directory"
	"github.com/openhpc-web/openhpc-web/internal/platform"
)

const (
	maxConcurrentDirectoryReads = 4
	maxDirectoryWebQuery        = 64
	directoryAuditTimeout       = time.Second
)

var errDirectoryBusy = errors.New("directory read limit reached")

func (a *application) ldapDirectory(c echo.Context) error {
	if c.QueryParam("q") != "" {
		if err := a.recordDirectoryAudit(c, "ldap.search", "denied"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	return a.searchLDAPDirectory(c, "")
}

func (a *application) ldapDirectorySearch(c echo.Context) error {
	query, err := normalizeDirectoryQuery(c.FormValue("q"))
	if err != nil {
		if auditErr := a.recordDirectoryAudit(c, "ldap.search", "denied"); auditErr != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	return a.searchLDAPDirectory(c, query)
}

func (a *application) searchLDAPDirectory(c echo.Context, query string) error {
	page := directory.Page{}
	available := false
	var err error
	if a.directoryProvider != nil {
		err = a.withDirectorySlot(func() error {
			var searchErr error
			page, searchErr = a.directoryProvider.Search(c.Request().Context(), query)
			return searchErr
		})
		if errors.Is(err, errDirectoryBusy) {
			if auditErr := a.recordDirectoryAudit(c, "ldap.search", "rate_limited"); auditErr != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable)
			}
			return echo.NewHTTPError(http.StatusTooManyRequests)
		}
		if err != nil {
			log.Printf("LDAP directory search failed")
		}
		available = err == nil
	}
	outcome := "unavailable"
	if available {
		outcome = "success"
	}
	if err := a.recordDirectoryAudit(c, "ldap.search", outcome); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	return a.renderLDAPDirectory(c, page, query, available)
}

func (a *application) ldapUser(c echo.Context) error {
	uid, err := decodeDirectoryKey(c.Param("uid"))
	if err != nil {
		if auditErr := a.recordDirectoryAudit(c, "ldap.user.read", "denied"); auditErr != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	user := directory.User{}
	found, available := false, false
	if a.directoryProvider != nil {
		err = a.withDirectorySlot(func() error {
			var readErr error
			user, found, readErr = a.directoryProvider.User(c.Request().Context(), uid)
			return readErr
		})
		if errors.Is(err, errDirectoryBusy) {
			if auditErr := a.recordDirectoryAudit(c, "ldap.user.read", "rate_limited"); auditErr != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable)
			}
			return echo.NewHTTPError(http.StatusTooManyRequests)
		}
		if err != nil {
			log.Printf("LDAP user lookup failed")
		}
		available = err == nil
	}
	if available && !found {
		if err := a.recordDirectoryAudit(c, "ldap.user.read", "denied"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusNotFound)
	}
	outcome := "unavailable"
	if available {
		outcome = "success"
	}
	if err := a.recordDirectoryAudit(c, "ldap.user.read", outcome); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	return a.renderLDAPUser(c, user, available)
}

func (a *application) ldapGroup(c echo.Context) error {
	name, err := decodeDirectoryKey(c.Param("name"))
	if err != nil {
		if auditErr := a.recordDirectoryAudit(c, "ldap.group.read", "denied"); auditErr != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	group := directory.Group{}
	found, available := false, false
	if a.directoryProvider != nil {
		err = a.withDirectorySlot(func() error {
			var readErr error
			group, found, readErr = a.directoryProvider.Group(c.Request().Context(), name)
			return readErr
		})
		if errors.Is(err, errDirectoryBusy) {
			if auditErr := a.recordDirectoryAudit(c, "ldap.group.read", "rate_limited"); auditErr != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable)
			}
			return echo.NewHTTPError(http.StatusTooManyRequests)
		}
		if err != nil {
			log.Printf("LDAP group lookup failed")
		}
		available = err == nil
	}
	if available && !found {
		if err := a.recordDirectoryAudit(c, "ldap.group.read", "denied"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusNotFound)
	}
	outcome := "unavailable"
	if available {
		outcome = "success"
	}
	if err := a.recordDirectoryAudit(c, "ldap.group.read", outcome); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	return a.renderLDAPGroup(c, group, available)
}

func (a *application) renderLDAPDirectory(c echo.Context, page directory.Page, query string, available bool) error {
	lang := language(c)
	labels := ldapCopyFor(lang)
	currentModule := moduleByPath("/ldap", lang)
	users := make([]ldapUserRow, len(page.Users))
	for index, user := range page.Users {
		users[index] = ldapUserRow{User: user, Key: encodeDirectoryKey(user.UID)}
	}
	groups := make([]ldapGroupRow, len(page.Groups))
	for index, group := range page.Groups {
		groups[index] = ldapGroupRow{Group: group, Key: encodeDirectoryKey(group.Name)}
	}
	view := ldapView{
		appChrome: a.newAppChrome(c, currentModule.Path, a.slurmHealth(c.Request().Context()), pageHeading{
			Eyebrow: "OPENHPC / LDAP", Title: currentModule.Label, Description: labels.Description,
			RefreshPath: "/ldap", RefreshLabel: labels.Refresh,
		}),
		Module: currentModule, Labels: labels, Page: page, Users: users, Groups: groups, Query: query, LDAPAvailable: available,
	}
	return a.render(c, http.StatusOK, "ldap.html", view)
}

func (a *application) renderLDAPUser(c echo.Context, user directory.User, available bool) error {
	lang := language(c)
	labels := ldapCopyFor(lang)
	currentModule := moduleByPath("/ldap", lang)
	view := ldapUserView{
		appChrome: a.newAppChrome(c, currentModule.Path, a.slurmHealth(c.Request().Context()), pageHeading{
			Eyebrow: "OPENHPC / LDAP", Title: labels.UserDetails, Description: user.UID,
		}),
		Module: currentModule, Labels: labels, User: user, LDAPAvailable: available,
	}
	return a.render(c, http.StatusOK, "ldap_user.html", view)
}

func (a *application) renderLDAPGroup(c echo.Context, group directory.Group, available bool) error {
	lang := language(c)
	labels := ldapCopyFor(lang)
	currentModule := moduleByPath("/ldap", lang)
	view := ldapGroupView{
		appChrome: a.newAppChrome(c, currentModule.Path, a.slurmHealth(c.Request().Context()), pageHeading{
			Eyebrow: "OPENHPC / LDAP", Title: labels.GroupDetails, Description: group.Name,
		}),
		Module: currentModule, Labels: labels, Group: group, LDAPAvailable: available,
	}
	return a.render(c, http.StatusOK, "ldap_group.html", view)
}

func (a *application) withDirectorySlot(operation func() error) error {
	select {
	case a.directorySlots <- struct{}{}:
		defer func() { <-a.directorySlots }()
		return operation()
	default:
		return errDirectoryBusy
	}
}

func normalizeDirectoryQuery(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > maxDirectoryWebQuery || !utf8.ValidString(value) {
		return "", errors.New("directory query is invalid")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("directory query contains control characters")
		}
	}
	return value, nil
}

func encodeDirectoryKey(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeDirectoryKey(value string) (string, error) {
	if len(value) == 0 || len(value) > 192 {
		return "", errors.New("directory key is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > 128 || !utf8.Valid(decoded) {
		return "", errors.New("directory key is invalid")
	}
	for _, character := range string(decoded) {
		if unicode.IsControl(character) {
			return "", errors.New("directory key is invalid")
		}
	}
	return string(decoded), nil
}

func (a *application) recordDirectoryAudit(c echo.Context, action, outcome string) error {
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), directoryAuditTimeout)
	defer cancel()
	if err := a.audit.Record(auditContext, platform.AuditEvent{Actor: a.username, Action: action, Outcome: outcome, CreatedAt: time.Now()}); err != nil {
		log.Printf("audit write failed for LDAP directory event")
		return err
	}
	return nil
}
