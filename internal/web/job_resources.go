package web

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

const maxConcurrentJobResourceReads = 4

func (a *application) slurmJobResources(c echo.Context) error {
	jobID, err := parsePositiveJobID(c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid job ID"})
	}
	if a.jobProvider == nil || a.jobResourceProvider == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "job resources unavailable"})
	}
	job, found, err := a.jobProvider.Job(c.Request().Context(), jobID)
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "job resources unavailable"})
	}
	if !found || job.ID != strconv.FormatInt(jobID, 10) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "job not found"})
	}
	if !canAccessJob(currentPrincipal(c), job) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "job not found"})
	}
	select {
	case a.jobResourceSlots <- struct{}{}:
		defer func() { <-a.jobResourceSlots }()
	default:
		return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "too many resource requests"})
	}
	usage, err := a.jobResourceProvider.JobResourceUsage(c.Request().Context(), jobID)
	if err != nil || usage.JobID != strconv.FormatInt(jobID, 10) {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "job resources unavailable"})
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, usage)
}
