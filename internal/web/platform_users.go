package web

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/acdiost/openhpc-web/internal/directory"
	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const maxBcryptPasswordBytes = 72

func (a *application) platformUsersPage(c echo.Context) error {
	users, err := a.platformUsers.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	rows := make([]platformUserRow, len(users))
	ldapProvisioning := ldapUserProvisioningVisible(a.directoryProvider)
	activeCount := 0
	confirmDisableUsername := ""
	for i, user := range users {
		if user.Enabled {
			activeCount++
		}
		role := "普通用户"
		if user.Role == platform.RoleAdmin {
			role = "管理员"
		}
		if language(c) == "en" {
			role = "Platform user"
			if user.Role == platform.RoleAdmin {
				role = "Administrator"
			}
		}
		if c.QueryParam("confirm") == "disable" && c.QueryParam("username") == user.Username && user.Enabled && user.Username != currentPrincipal(c).Username {
			confirmDisableUsername = user.Username
		}
		rows[i] = platformUserRow{Username: user.Username, Role: role, Enabled: user.Enabled, CanCreateLDAP: ldapProvisioning && user.Enabled, CreatedAt: user.CreatedAt.Local().Format("2006-01-02 15:04")}
	}
	ldapCreateUsername := ""
	if candidate := c.QueryParam("ldap_user"); platform.ValidateUsername(candidate) == nil {
		for _, row := range rows {
			if row.Username == candidate && row.CanCreateLDAP {
				ldapCreateUsername = candidate
				break
			}
		}
	}
	ldapGroups := make([]directory.Group, 0)
	ldapGroupsAvailable := false
	if ldapProvisioning {
		var ldapPage directory.Page
		if err := a.withDirectorySlot(func() error {
			var searchErr error
			ldapPage, searchErr = a.directoryProvider.Search(c.Request().Context(), "")
			return searchErr
		}); err != nil {
			log.Printf("LDAP group list unavailable for user provisioning")
		} else {
			ldapGroupsAvailable = true
			for _, group := range ldapPage.Groups {
				if group.GIDNumber >= 1000 {
					ldapGroups = append(ldapGroups, group)
				}
			}
		}
	}
	return a.render(c, http.StatusOK, "platform_users.html", platformUsersView{
		appChrome:              a.newAppChrome(c, "/platform/users", a.slurmHealth(c.Request().Context()), pageHeading{Eyebrow: "OPENHPC / IDENTITY", Title: map[bool]string{true: "Platform users", false: "平台用户"}[language(c) == "en"], Description: map[bool]string{true: "Manage local platform accounts", false: "管理平台本地账号"}[language(c) == "en"]}),
		Users:                  rows,
		TotalCount:             len(rows),
		ActiveCount:            activeCount,
		DisabledCount:          len(rows) - activeCount,
		ConfirmDisableUsername: confirmDisableUsername,
		OpenCreate:             c.QueryParam("create") == "1",
		LDAPProvisioning:       ldapProvisioning,
		LDAPCreateUsername:     ldapCreateUsername,
		LDAPCreateHome:         "/home/" + ldapCreateUsername,
		LDAPGroups:             ldapGroups,
		LDAPGroupsAvailable:    ldapGroupsAvailable,
		Success:                platformUserSuccessFor(language(c), c.QueryParam("result")),
		Error:                  platformUserErrorFor(language(c), c.QueryParam("result")),
	})
}

func (a *application) createLinkedLDAPUser(c echo.Context) error {
	creator, supported := a.directoryProvider.(directory.UserCreator)
	if !supported || !ldapUserProvisioningAvailable(a.directoryProvider) {
		a.recordLDAPUserCreateAudit(c, "unavailable")
		return a.redirectPlatformUsers(c, "ldap-unavailable")
	}
	uidNumber, uidErr := strconv.ParseInt(c.FormValue("uid_number"), 10, 64)
	gidNumber, gidErr := strconv.ParseInt(c.FormValue("gid_number"), 10, 64)
	request := directory.UserCreateRequest{
		UID:           strings.TrimSpace(c.FormValue("username")),
		UIDNumber:     uidNumber,
		GIDNumber:     gidNumber,
		HomeDirectory: strings.TrimSpace(c.FormValue("home_directory")),
		LoginShell:    strings.TrimSpace(c.FormValue("login_shell")),
		Password:      c.FormValue("password"),
	}
	if uidErr != nil || gidErr != nil || directory.ValidateUserCreateRequest(request) != nil {
		a.recordLDAPUserCreateAudit(c, "invalid_request")
		return a.redirectPlatformUsers(c, "ldap-invalid")
	}
	platformUser, found, err := a.platformUsers.Get(c.Request().Context(), request.UID)
	if err != nil || !found || !platformUser.Enabled {
		a.recordLDAPUserCreateAudit(c, "denied")
		return a.redirectPlatformUsers(c, "ldap-invalid")
	}
	if err := a.withDirectorySlot(func() error { return creator.CreateUser(c.Request().Context(), request) }); err != nil {
		log.Printf("LDAP user create failed")
		a.recordLDAPUserCreateAudit(c, "failure")
		return a.redirectPlatformUsers(c, "ldap-error")
	}
	a.recordLDAPUserCreateAudit(c, "success")
	return a.redirectPlatformUsers(c, "ldap-created")
}

func ldapUserProvisioningAvailable(provider directory.Provider) bool {
	creator, supported := provider.(directory.UserCreator)
	if !supported {
		return false
	}
	status, reportsStatus := creator.(directory.UserProvisioningStatus)
	return !reportsStatus || status.UserProvisioningAvailable()
}

func ldapUserProvisioningVisible(provider directory.Provider) bool {
	creator, supported := provider.(directory.UserCreator)
	if !supported {
		return false
	}
	status, reportsStatus := creator.(directory.UserProvisioningSupport)
	return !reportsStatus || status.UserProvisioningSupported()
}

func (a *application) recordLDAPUserCreateAudit(c echo.Context, outcome string) {
	_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "ldap.user.create", Outcome: outcome, CreatedAt: time.Now()})
}

func (a *application) createPlatformUser(c echo.Context) error {
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	role := platform.UserRole(c.FormValue("role"))
	if err := platform.ValidateUsername(username); err != nil || !validPlatformUserPassword(password) || (role != platform.RoleUser && role != platform.RoleAdmin) {
		return a.redirectPlatformUsers(c, "invalid")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	if err := a.platformUsers.Create(c.Request().Context(), platform.PlatformUser{Username: username, PasswordHash: string(hash), Role: role, Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		if errors.Is(err, platform.ErrUserExists) {
			return a.redirectPlatformUsers(c, "duplicate")
		}
		return a.redirectPlatformUsers(c, "error")
	}
	_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "platform.user.create", Outcome: "success", CreatedAt: time.Now()})
	return a.redirectPlatformUsers(c, "created")
}

func (a *application) setPlatformUserStatus(c echo.Context) error {
	username := strings.TrimSpace(c.FormValue("username"))
	if username == currentPrincipal(c).Username {
		return a.redirectPlatformUsers(c, "invalid")
	}
	enabledValue := c.FormValue("enabled")
	if enabledValue != "true" && enabledValue != "false" {
		return a.redirectPlatformUsers(c, "invalid")
	}
	enabled := enabledValue == "true"
	if !enabled && c.FormValue("confirmed") != "true" {
		return c.Redirect(http.StatusSeeOther, "/platform/users?confirm=disable&username="+url.QueryEscape(username))
	}
	if err := a.platformUsers.SetEnabled(c.Request().Context(), username, enabled); err != nil {
		return a.redirectPlatformUsers(c, "error")
	}
	if !enabled {
		a.sessions.removeUsername(username)
	}
	_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "platform.user.status", Outcome: "success", CreatedAt: time.Now()})
	return a.redirectPlatformUsers(c, "updated")
}

func (a *application) redirectPlatformUsers(c echo.Context, result string) error {
	return c.Redirect(http.StatusSeeOther, "/platform/users?result="+result)
}

func platformUserSuccessFor(language, result string) string {
	if language == "en" {
		switch result {
		case "created":
			return "Platform user created."
		case "updated":
			return "Platform user status updated."
		case "ldap-created":
			return "LDAP user created for the platform account."
		}
		return ""
	}
	switch result {
	case "created":
		return "平台用户已创建。"
	case "updated":
		return "平台用户状态已更新。"
	case "ldap-created":
		return "已为平台账号创建 LDAP 用户。"
	}
	return ""
}

func platformUserErrorFor(language, result string) string {
	if language == "en" {
		switch result {
		case "duplicate":
			return "A platform user with this username already exists."
		case "invalid":
			return "The submitted platform user details are invalid."
		case "error":
			return "The platform user could not be saved."
		case "ldap-invalid":
			return "The LDAP user details are invalid or the platform account is unavailable."
		case "ldap-unavailable":
			return "LDAP user provisioning is unavailable."
		case "ldap-error":
			return "The LDAP user could not be created."
		}
		return ""
	}
	switch result {
	case "duplicate":
		return "同名平台用户已存在。"
	case "invalid":
		return "提交的平台用户信息无效。"
	case "error":
		return "无法保存平台用户。"
	case "ldap-invalid":
		return "LDAP 用户信息无效，或平台账号不可用。"
	case "ldap-unavailable":
		return "LDAP 用户创建当前不可用。"
	case "ldap-error":
		return "无法创建 LDAP 用户。"
	}
	return ""
}

func validPlatformUserPassword(password string) bool {
	return len(password) >= 12 && len([]byte(password)) <= maxBcryptPasswordBytes
}
