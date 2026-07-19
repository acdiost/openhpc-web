package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/acdiost/openhpc-web/internal/cluster"
	"github.com/acdiost/openhpc-web/internal/platform"
	"github.com/labstack/echo/v4"
	"golang.org/x/sys/unix"
)

const (
	maxJobOutputBytes           int64 = 256 << 10
	maxConcurrentJobOutputReads       = 4
	jobOutputReadTimeout              = 3 * time.Second
	jobOutputAuditTimeout             = time.Second
)

var errJobOutputUnavailable = errors.New("job output unavailable")

func (a *application) slurmJobOutput(c echo.Context) error {
	jobID, err := parsePositiveJobID(c.Param("id"))
	stream := c.Param("stream")
	if err != nil || (stream != "stdout" && stream != "stderr") {
		if auditErr := a.recordInvalidJobOutputAudit(c); auditErr != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusBadRequest)
	}
	if a.jobProvider == nil {
		if err := a.recordJobOutputAudit(c, jobID, stream, "unavailable"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	slotOwnedByHandler := false
	select {
	case a.jobOutputSlots <- struct{}{}:
		slotOwnedByHandler = true
	default:
		if err := a.recordJobOutputAudit(c, jobID, stream, "rate_limited"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusTooManyRequests)
	}
	defer func() {
		if slotOwnedByHandler {
			<-a.jobOutputSlots
		}
	}()

	job, found, err := a.jobProvider.Job(c.Request().Context(), jobID)
	if err != nil {
		if auditErr := a.recordJobOutputAudit(c, jobID, stream, "unavailable"); auditErr != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if !found || job.ID != strconv.FormatInt(jobID, 10) {
		if err := a.recordJobOutputAudit(c, jobID, stream, "denied"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusNotFound)
	}
	if !canAccessJob(currentPrincipal(c), job) {
		if err := a.recordJobOutputAudit(c, jobID, stream, "denied"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusNotFound)
	}

	readContext, cancelRead := context.WithTimeout(c.Request().Context(), jobOutputReadTimeout)
	defer cancelRead()
	type readResult struct {
		content   []byte
		truncated bool
		err       error
	}
	resultChannel := make(chan readResult, 1)
	slotOwnedByHandler = false
	go func() {
		defer func() { <-a.jobOutputSlots }()
		content, truncated, err := readJobOutput(readContext, job, stream)
		resultChannel <- readResult{content: content, truncated: truncated, err: err}
	}()

	var result readResult
	select {
	case result = <-resultChannel:
	case <-readContext.Done():
		outcome := "cancelled"
		status := http.StatusRequestTimeout
		if errors.Is(readContext.Err(), context.DeadlineExceeded) {
			outcome = "timeout"
			status = http.StatusGatewayTimeout
		}
		if err := a.recordJobOutputAudit(c, jobID, stream, outcome); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(status)
	}
	if result.err != nil {
		log.Printf("job output read failed: job_id=%d stream=%s user_id=%d reason=%v", jobID, stream, job.UserID, result.err)
		if errors.Is(result.err, context.Canceled) || errors.Is(result.err, context.DeadlineExceeded) {
			outcome := "cancelled"
			status := http.StatusRequestTimeout
			if errors.Is(result.err, context.DeadlineExceeded) {
				outcome = "timeout"
				status = http.StatusGatewayTimeout
			}
			if err := a.recordJobOutputAudit(c, jobID, stream, outcome); err != nil {
				return echo.NewHTTPError(http.StatusServiceUnavailable)
			}
			return echo.NewHTTPError(status)
		}
		if err := a.recordJobOutputAudit(c, jobID, stream, "denied"); err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable)
		}
		return echo.NewHTTPError(http.StatusNotFound)
	}
	if err := a.recordJobOutputAudit(c, jobID, stream, "success"); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable)
	}
	if result.truncated {
		c.Response().Header().Set("X-Content-Truncated", "true")
	}
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", result.content)
}

func parsePositiveJobID(value string) (int64, error) {
	if value == "" || len(value) > 19 || value[0] == '0' {
		return 0, errors.New("invalid job ID")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("invalid job ID")
		}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid job ID")
	}
	return id, nil
}

func readJobOutput(ctx context.Context, job cluster.Job, stream string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	path := job.StdOut
	if stream == "stderr" {
		path = job.StdErr
	}
	if !validJobOutputPath(path) {
		return nil, false, errJobOutputUnavailable
	}
	return readJobOutputAsUser(ctx, job.UserID, job.GroupID, path)
}

func validJobOutputPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func readJobOutputAtCurrentIdentity(ctx context.Context, path string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !validJobOutputPath(path) {
		return nil, false, errJobOutputUnavailable
	}
	file, stat, err := openAbsoluteFileNoFollow(path)
	if err != nil {
		return nil, false, fmt.Errorf("open output file: %w", err)
	}
	defer file.Close()

	offset := stat.Size - maxJobOutputBytes
	truncated := offset > 0
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, 0); err != nil {
		return nil, false, fmt.Errorf("seek output file: %w", err)
	}
	content, err := readBoundedOutput(ctx, file)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("read output file: %w", err)
	}
	content = bytes.ToValidUTF8(content, []byte("\uFFFD"))
	if int64(len(content)) > maxJobOutputBytes {
		start := len(content) - int(maxJobOutputBytes)
		for start < len(content) && !utf8.RuneStart(content[start]) {
			start++
		}
		content, truncated = content[start:], true
	}
	return content, truncated, nil
}

func openAbsoluteFileNoFollow(path string) (*os.File, *unix.Stat_t, error) {
	if !validJobOutputPath(path) {
		return nil, nil, errJobOutputUnavailable
	}
	rootFD, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	defer unix.Close(rootFD)
	return openRelativeFileNoFollow(rootFD, strings.TrimPrefix(path, string(filepath.Separator)))
}

func openRelativeFileNoFollow(rootFD int, relativePath string) (*os.File, *unix.Stat_t, error) {
	duplicate, err := duplicateCloseOnExec(rootFD)
	if err != nil {
		return nil, nil, err
	}
	fileFD, err := walkOpenNoFollow(duplicate, relativePath, false)
	if err != nil {
		return nil, nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fileFD, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fileFD)
		return nil, nil, errJobOutputUnavailable
	}
	return os.NewFile(uintptr(fileFD), relativePath), &stat, nil
}

func duplicateCloseOnExec(fd int) (int, error) {
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}

func walkOpenNoFollow(startFD int, relativePath string, finalDirectory bool) (int, error) {
	currentFD := startFD
	components := strings.Split(relativePath, string(filepath.Separator))
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			unix.Close(currentFD)
			return -1, errJobOutputUnavailable
		}
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if index < len(components)-1 || finalDirectory {
			flags |= unix.O_DIRECTORY
		}
		nextFD, err := unix.Openat(currentFD, component, flags, 0)
		unix.Close(currentFD)
		if err != nil {
			return -1, err
		}
		currentFD = nextFD
	}
	return currentFD, nil
}

func readBoundedOutput(ctx context.Context, file *os.File) ([]byte, error) {
	result := make([]byte, 0, int(maxJobOutputBytes))
	buffer := make([]byte, 32<<10)
	for int64(len(result)) < maxJobOutputBytes {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		remaining := int(maxJobOutputBytes) - len(result)
		if remaining < len(buffer) {
			buffer = buffer[:remaining]
		}
		count, err := file.Read(buffer)
		result = append(result, buffer[:count]...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if count == 0 {
			break
		}
	}
	return result, nil
}

func (a *application) recordJobOutputAudit(c echo.Context, jobID int64, stream, outcome string) error {
	return a.recordJobOutputAuditEvent(c, fmt.Sprintf("slurm.job_output.%s:%d", stream, jobID), outcome)
}

func (a *application) recordInvalidJobOutputAudit(c echo.Context) error {
	return a.recordJobOutputAuditEvent(c, "slurm.job_output.invalid_request", "denied")
}

func (a *application) recordJobOutputAuditEvent(c echo.Context, action, outcome string) error {
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(c.Request().Context()), jobOutputAuditTimeout)
	defer cancel()
	err := a.audit.Record(auditContext, platform.AuditEvent{
		Actor: currentPrincipal(c).Username, Action: action,
		Outcome: outcome, CreatedAt: time.Now(),
	})
	if err != nil {
		log.Printf("audit write failed for job output event")
	}
	return err
}
