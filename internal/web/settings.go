package web

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/acdiost/openhpc-web/internal/ldapdirectory"
	"github.com/acdiost/openhpc-web/internal/platform"
	ldap "github.com/go-ldap/ldap/v3"
	"github.com/labstack/echo/v4"
)

type settingsSpec struct {
	Key, GroupZH, GroupEN, LabelZH, LabelEN, InputType string
	Secret                                             bool
}

var settingsSpecs = []settingsSpec{
	{Key: "OPENHPC_SLURM_ENABLED", GroupZH: "Slurm", GroupEN: "Slurm", LabelZH: "启用 Slurm", LabelEN: "Enable Slurm", InputType: "checkbox"},
	{Key: "OPENHPC_SLURM_BIN_DIR", GroupZH: "Slurm", GroupEN: "Slurm", LabelZH: "Slurm 二进制目录", LabelEN: "Slurm binary directory", InputType: "text"},
	{Key: "OPENHPC_SLURM_TIMEOUT", GroupZH: "Slurm", GroupEN: "Slurm", LabelZH: "Slurm 超时", LabelEN: "Slurm timeout", InputType: "text"},
	{Key: "OPENHPC_SLURM_MAX_OUTPUT", GroupZH: "Slurm", GroupEN: "Slurm", LabelZH: "最大输出字节数", LabelEN: "Maximum output bytes", InputType: "number"},
	{Key: "OPENHPC_SLURM_CACHE_TTL", GroupZH: "Slurm", GroupEN: "Slurm", LabelZH: "缓存 TTL", LabelEN: "Cache TTL", InputType: "text"},
	{Key: "OPENHPC_JOB_OUTPUT_ROOTS", GroupZH: "Slurm", GroupEN: "Slurm", LabelZH: "作业输出根目录", LabelEN: "Job output roots", InputType: "text"},
	{Key: "OPENHPC_LDAP_ENABLED", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "启用 LDAP", LabelEN: "Enable LDAP", InputType: "checkbox"},
	{Key: "OPENHPC_LDAP_URL", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "LDAP 地址", LabelEN: "LDAP URL", InputType: "url"},
	{Key: "OPENHPC_LDAP_BASE_DN", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "基础 DN", LabelEN: "Base DN", InputType: "text"},
	{Key: "OPENHPC_LDAP_USER_BASE_DN", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "用户基础 DN", LabelEN: "User base DN", InputType: "text"},
	{Key: "OPENHPC_LDAP_GROUP_BASE_DN", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "组基础 DN", LabelEN: "Group base DN", InputType: "text"},
	{Key: "OPENHPC_LDAP_BIND_DN", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "只读 Bind DN", LabelEN: "Read-only bind DN", InputType: "text"},
	{Key: "OPENHPC_LDAP_BIND_PASSWORD", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "Bind 密码", LabelEN: "Bind password", InputType: "password", Secret: true},
	{Key: "OPENHPC_LDAP_CA_FILE", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "CA 文件", LabelEN: "CA file", InputType: "text"},
	{Key: "OPENHPC_LDAP_TIMEOUT", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "LDAP 超时", LabelEN: "LDAP timeout", InputType: "text"},
	{Key: "OPENHPC_LDAP_MAX_RESULTS", GroupZH: "LDAP", GroupEN: "LDAP", LabelZH: "最大结果数", LabelEN: "Maximum results", InputType: "number"},
}

type settingFieldView struct {
	Key, Label, Group, Value, Source string
	InputType                        string
	Secret, Configured               bool
}

type settingsView struct {
	appChrome
	Groups         []settingsGroupView
	Error, Success string
}

type settingsGroupView struct {
	Name   string
	Fields []settingFieldView
}

func (a *application) settingsPage(c echo.Context) error {
	view, err := a.settingsView(c, c.QueryParam("updated") == "1", "")
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	return a.render(c, http.StatusOK, "settings.html", view)
}

func (a *application) settingsView(c echo.Context, updated bool, message string) (settingsView, error) {
	lang := language(c)
	overrides := map[string]platform.Setting{}
	if a.settingsStore != nil {
		keys := make(map[string]bool, len(settingsSpecs))
		for _, spec := range settingsSpecs {
			keys[spec.Key] = true
		}
		entries, err := a.settingsStore.List(c.Request().Context(), keys)
		if err != nil {
			return settingsView{}, err
		}
		for _, entry := range entries {
			overrides[entry.Key] = entry
		}
	}
	fields := make([]settingFieldView, 0, len(settingsSpecs))
	for _, spec := range settingsSpecs {
		value, source, configured := a.settingsDefaults[spec.Key], "环境变量", strings.TrimSpace(a.settingsDefaults[spec.Key]) != ""
		if lang == "en" {
			source = "Environment"
		}
		if override, ok := overrides[spec.Key]; ok {
			source, configured = "SQLite override", true
			if !spec.Secret {
				value = override.Value
			}
		}
		if spec.Secret {
			value = ""
		}
		label, group := spec.LabelZH, spec.GroupZH
		if lang == "en" {
			label, group = spec.LabelEN, spec.GroupEN
		}
		fields = append(fields, settingFieldView{Key: spec.Key, Label: label, Group: group, Value: value, Source: source, InputType: spec.InputType, Secret: spec.Secret, Configured: configured})
	}
	success := message
	if updated && success == "" {
		if lang == "en" {
			success = "Settings saved. Restart the service to apply changes."
		} else {
			success = "设置已保存，重启服务后生效。"
		}
	}
	groups := make([]settingsGroupView, 0, 2)
	for _, field := range fields {
		if len(groups) == 0 || groups[len(groups)-1].Name != field.Group {
			groups = append(groups, settingsGroupView{Name: field.Group})
		}
		groups[len(groups)-1].Fields = append(groups[len(groups)-1].Fields, field)
	}
	return settingsView{appChrome: a.newAppChrome(c, "/settings", a.slurmHealth(c.Request().Context()), pageHeading{Eyebrow: "OPENHPC / SYSTEM", Title: map[bool]string{true: "System settings", false: "系统设置"}[lang == "en"], Description: map[bool]string{true: "Edit persisted Slurm and LDAP overrides", false: "编辑持久化的 Slurm 与 LDAP 覆盖配置"}[lang == "en"]}), Groups: groups, Success: success, Error: message}, nil
}

func (a *application) saveSettings(c echo.Context) error {
	if a.settingsStore == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if err := c.Request().ParseForm(); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	known := make(map[string]settingsSpec, len(settingsSpecs))
	values := make(map[string]string)
	for _, spec := range settingsSpecs {
		known[spec.Key] = spec
	}
	for key := range c.Request().PostForm {
		if key == "_csrf" || strings.HasPrefix(key, "clear_") {
			if strings.HasPrefix(key, "clear_") {
				if _, ok := known[strings.TrimPrefix(key, "clear_")]; !ok {
					return echo.NewHTTPError(http.StatusBadRequest)
				}
			}
			continue
		}
		if _, ok := known[key]; !ok {
			return echo.NewHTTPError(http.StatusBadRequest)
		}
	}
	for _, spec := range settingsSpecs {
		if spec.Secret {
			if c.FormValue("clear_"+spec.Key) == "1" {
				values[spec.Key] = ""
				continue
			}
			if c.FormValue(spec.Key) == "" {
				continue
			}
		} else if _, present := c.Request().PostForm[spec.Key]; !present {
			continue
		}
		value := c.FormValue(spec.Key)
		if spec.InputType == "checkbox" && value == "" {
			value = "false"
		}
		if err := validateSettingFormValue(spec.Key, value); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest)
		}
		values[spec.Key] = value
	}
	if err := a.settingsStore.SetMany(c.Request().Context(), values); err != nil {
		_ = a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "settings.update", Outcome: "failed", CreatedAt: time.Now()})
		if errors.Is(err, platform.ErrSettingsKeyRequired) {
			message := "保存秘密字段前必须配置 OPENHPC_SETTINGS_KEY。"
			if language(c) == "en" {
				message = "Configure OPENHPC_SETTINGS_KEY before saving secret fields."
			}
			view, viewErr := a.settingsView(c, false, message)
			if viewErr != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable)
			}
			return a.render(c, http.StatusServiceUnavailable, "settings.html", view)
		}
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if err := a.recordAudit(c, platform.AuditEvent{Actor: currentPrincipal(c).Username, Action: "settings.update", Outcome: "success", CreatedAt: time.Now()}); err != nil {
		log.Printf("settings audit failed")
	}
	return c.Redirect(http.StatusSeeOther, "/settings?updated=1")
}

func validateSettingFormValue(key, value string) error {
	if len(value) > 8192 || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("invalid setting value")
	}
	switch key {
	case "OPENHPC_SLURM_ENABLED", "OPENHPC_LDAP_ENABLED":
		if value != "true" && value != "false" {
			return errors.New("invalid boolean")
		}
	case "OPENHPC_SLURM_TIMEOUT", "OPENHPC_SLURM_CACHE_TTL", "OPENHPC_LDAP_TIMEOUT":
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 || duration > 60*time.Second {
			return errors.New("invalid duration")
		}
	case "OPENHPC_SLURM_MAX_OUTPUT":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 8<<20 {
			return errors.New("invalid output limit")
		}
	case "OPENHPC_LDAP_MAX_RESULTS":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 500 {
			return errors.New("invalid result limit")
		}
	case "OPENHPC_LDAP_URL":
		if value != "" {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme != "ldaps" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
				return errors.New("invalid LDAP URL")
			}
		}
	case "OPENHPC_LDAP_BASE_DN", "OPENHPC_LDAP_USER_BASE_DN", "OPENHPC_LDAP_GROUP_BASE_DN", "OPENHPC_LDAP_BIND_DN":
		if value != "" {
			if _, err := ldap.ParseDN(value); err != nil {
				return errors.New("invalid DN")
			}
		}
	case "OPENHPC_SLURM_BIN_DIR", "OPENHPC_LDAP_CA_FILE":
		if value != "" && (!filepath.IsAbs(value) || filepath.Clean(value) != value) {
			return errors.New("invalid path")
		}
		if key == "OPENHPC_LDAP_CA_FILE" && value != "" {
			if err := ldapdirectory.ValidateCAFile(value); err != nil {
				return errors.New("invalid CA file")
			}
		}
	case "OPENHPC_JOB_OUTPUT_ROOTS":
		for _, root := range strings.Split(value, ",") {
			root = strings.TrimSpace(root)
			if root != "" && (!filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator)) {
				return errors.New("invalid output root")
			}
			if root != "" {
				info, err := os.Lstat(root)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return errors.New("invalid output root")
				}
			}
		}
	}
	return nil
}

func cloneSettings(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
