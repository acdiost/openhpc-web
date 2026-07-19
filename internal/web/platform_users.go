package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

func (a *application) platformUsersPage(c echo.Context) error {
	users, err := a.platformUsers.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	rows := make([]platformUserRow, len(users))
	for i, user := range users {
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
		rows[i] = platformUserRow{Username: user.Username, Role: role, Enabled: user.Enabled, CreatedAt: user.CreatedAt.Local().Format("2006-01-02 15:04")}
	}
	return a.render(c, http.StatusOK, "platform_users.html", platformUsersView{
		appChrome: a.newAppChrome(c, "/platform/users", a.slurmHealth(c.Request().Context()), pageHeading{Eyebrow: "OPENHPC / IDENTITY", Title: map[bool]string{true: "Platform users", false: "平台用户"}[language(c) == "en"], Description: map[bool]string{true: "Manage local platform accounts", false: "管理平台本地账号"}[language(c) == "en"]}),
		Users:     rows,
	})
}

func (a *application) createPlatformUser(c echo.Context) error {
	username := strings.TrimSpace(c.FormValue("username"))
	password := c.FormValue("password")
	role := platform.UserRole(c.FormValue("role"))
	if err := platform.ValidateUsername(username); err != nil || len(password) < 12 || len(password) > 256 || (role != platform.RoleUser && role != platform.RoleAdmin) {
		return a.redirectPlatformUsers(c, "invalid")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	if err := a.platformUsers.Upsert(c.Request().Context(), platform.PlatformUser{Username: username, PasswordHash: string(hash), Role: role, Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
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
	enabled := c.FormValue("enabled") == "true"
	if err := a.platformUsers.SetEnabled(c.Request().Context(), username, enabled); err != nil {
		return a.redirectPlatformUsers(c, "error")
	}
	_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "platform.user.status", Outcome: "success", CreatedAt: time.Now()})
	return a.redirectPlatformUsers(c, "updated")
}

func (a *application) redirectPlatformUsers(c echo.Context, result string) error {
	return c.Redirect(http.StatusSeeOther, "/platform/users?result="+result)
}
