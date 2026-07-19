package slurm

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func (c *Client) CancelJob(parent context.Context, id int64) error {
	if id <= 0 {
		return errors.New("job ID must be positive")
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	jobID := strconv.FormatInt(id, 10)
	if _, err := c.run(ctx, "scancel", jobID); err != nil {
		return fmt.Errorf("cancel Slurm job %s: %w", jobID, err)
	}
	c.jobsCache.invalidate()
	return nil
}

var _ cluster.JobCanceler = (*Client)(nil)
