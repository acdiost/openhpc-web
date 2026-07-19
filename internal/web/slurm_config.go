package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/acdiost/openhpc-web/internal/slurmconfig"
)

const slurmConfigAuditTimeout = time.Second

func (a *application) slurmConfig(c echo.Context) error {
	fileName := c.QueryParam("file")
	if fileName != "" && !slurmconfig.ValidName(fileName) {
		if err := a.recordSlurmConfigAudit(c, "invalid_request"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	entries := []slurmconfig.Entry(nil)
	var selected *slurmConfigFileView
	available := false
	if a.slurmConfigProvider != nil {
		list, err := a.slurmConfigProvider.List(c.Request().Context())
		if err != nil {
			log.Printf("Slurm config listing failed")
		} else {
			entries, available = list, true
			if fileName != "" {
				file, readErr := a.slurmConfigProvider.Read(c.Request().Context(), fileName)
				if readErr != nil {
					if auditErr := a.recordSlurmConfigAudit(c, "denied"); auditErr != nil {
						return echo.NewHTTPError(http.StatusServiceUnavailable)
					}
					return echo.NewHTTPError(http.StatusNotFound)
				}
				selected = &slurmConfigFileView{Name: file.Name, Size: file.Size, Content: file.Content, Truncated: file.Truncated}
			}
		}
	}
	outcome := "unavailable"
	if available {
		outcome = "success"
	}
	if err := a.recordSlurmConfigAudit(c, outcome); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	lang := language(c)
	labels := slurmConfigCopyFor(lang)
	module := moduleByPath("/slurm/config", lang)
	view := slurmConfigView{
		appChrome: a.newAppChrome(c, module.Path, a.slurmHealth(c.Request().Context()), pageHeading{
			Eyebrow: "OPENHPC / SLURM", Title: module.Label, Description: labels.Description,
			RefreshPath: "/slurm/config", RefreshLabel: labels.Refresh,
		}),
		Module: module, Labels: labels, Entries: entries, Selected: selected, Available: available,
	}
	return a.render(c, http.StatusOK, "slurm_config.html", view)
}

func (a *application) recordSlurmConfigAudit(c echo.Context, outcome string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), slurmConfigAuditTimeout)
	defer cancel()
	if err := a.audit.Record(ctx, platform.AuditEvent{Actor: a.username, Action: "slurm.config.read", Outcome: outcome, CreatedAt: time.Now()}); err != nil {
		log.Printf("audit write failed for Slurm config event")
		return errors.New("audit unavailable")
	}
	return nil
}
