package web

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/labstack/echo/v4"
)

func (a *application) slurmJobCancel(c echo.Context) error {
	jobID, err := parsePositiveJobID(c.Param("id"))
	if err != nil {
		a.recordJobCancelAudit(c, 0, "invalid_request")
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid job ID"})
	}
	if a.jobProvider == nil || a.jobCanceler == nil {
		a.recordJobCancelAudit(c, jobID, "unavailable")
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "job cancellation unavailable"})
	}
	job, found, err := a.jobProvider.Job(c.Request().Context(), jobID)
	if err != nil {
		log.Printf("Slurm job cancellation lookup failed")
		a.recordJobCancelAudit(c, jobID, "unavailable")
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "job cancellation unavailable"})
	}
	if !found || job.ID != strconv.FormatInt(jobID, 10) || !canCancelJob(currentPrincipal(c), job) {
		a.recordJobCancelAudit(c, jobID, "denied")
		return c.JSON(http.StatusNotFound, map[string]string{"error": "job not found"})
	}
	if err := a.jobCanceler.CancelJob(c.Request().Context(), jobID); err != nil {
		log.Printf("Slurm job cancellation failed")
		a.recordJobCancelAudit(c, jobID, "failed")
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "job cancellation unavailable"})
	}
	a.recordJobCancelAudit(c, jobID, "success")
	return c.Redirect(http.StatusSeeOther, "/slurm/jobs")
}

func (a *application) recordJobCancelAudit(c echo.Context, jobID int64, outcome string) {
	if err := a.recordAudit(c, platform.AuditEvent{
		Actor: currentPrincipal(c).Username, Action: "slurm.job.cancel:" + strconv.FormatInt(jobID, 10), Outcome: outcome, CreatedAt: time.Now(),
	}); err != nil {
		log.Printf("job cancellation audit failed")
	}
}
