package slurm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

type coreHoursJSON struct {
	Errors []json.RawMessage `json:"errors"`
	Jobs   []struct {
		Account   string `json:"account"`
		User      string `json:"user"`
		Partition string `json:"partition"`
		Time      struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"time"`
		State struct {
			Current []string `json:"current"`
		} `json:"state"`
		TRES struct {
			Allocated []struct {
				Type  string `json:"type"`
				Count int64  `json:"count"`
			} `json:"allocated"`
		} `json:"tres"`
	} `json:"jobs"`
}

func (c *Client) CoreHours(parent context.Context, period cluster.CoreHourPeriod) (cluster.CoreHourSummary, error) {
	duration, cacheIndex, err := coreHourWindow(period)
	if err != nil {
		return cluster.CoreHourSummary{}, err
	}
	summary, err := c.coreHourCaches[cacheIndex].get(parent, func(ctx context.Context) (cluster.CoreHourSummary, error) {
		to := c.now()
		from := to.Add(-duration)
		commandCtx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		const slurmTimeLayout = "2006-01-02T15:04:05"
		output, err := c.run(commandCtx, "sacct", "--json", "--allocations", "--allusers", "--starttime="+from.Format(slurmTimeLayout), "--endtime="+to.Format(slurmTimeLayout))
		if err != nil {
			return cluster.CoreHourSummary{}, fmt.Errorf("read Slurm core hours: %w", err)
		}
		return parseCoreHoursJSON(output, from, to)
	})
	return cloneCoreHourSummary(summary), err
}

func coreHourWindow(period cluster.CoreHourPeriod) (time.Duration, int, error) {
	switch period {
	case cluster.CoreHourPeriod24Hours:
		return 24 * time.Hour, 0, nil
	case cluster.CoreHourPeriod7Days:
		return 7 * 24 * time.Hour, 1, nil
	case cluster.CoreHourPeriod30Days:
		return 30 * 24 * time.Hour, 2, nil
	default:
		return 0, 0, errors.New("unsupported core-hour period")
	}
}

func parseCoreHoursJSON(output []byte, from, to time.Time) (cluster.CoreHourSummary, error) {
	var response coreHoursJSON
	if err := json.Unmarshal(output, &response); err != nil {
		return cluster.CoreHourSummary{}, fmt.Errorf("decode core-hour JSON: %w", err)
	}
	if len(response.Errors) > 0 {
		return cluster.CoreHourSummary{}, fmt.Errorf("core-hour JSON reported %d errors", len(response.Errors))
	}
	fromUnix, toUnix := from.Unix(), to.Unix()
	if toUnix <= fromUnix {
		return cluster.CoreHourSummary{}, errors.New("invalid core-hour window")
	}
	summary := cluster.CoreHourSummary{From: from, To: to}
	accounts := make(map[string]cluster.CoreHourGroup)
	users := make(map[string]cluster.CoreHourGroup)
	for _, job := range response.Jobs {
		if err := validateDetailStrings([]string{job.Account, job.User, job.Partition}); err != nil {
			return cluster.CoreHourSummary{}, err
		}
		if job.Time.Start <= 0 {
			continue
		}
		end := job.Time.End
		if end == 0 {
			if !containsFold(job.State.Current, "RUNNING") {
				continue
			}
			end = toUnix
		}
		start := maxInt64(job.Time.Start, fromUnix)
		end = minInt64(end, toUnix)
		if end <= start {
			continue
		}
		cpus, err := allocatedCPUs(job.TRES.Allocated)
		if err != nil {
			return cluster.CoreHourSummary{}, err
		}
		if cpus == 0 {
			continue
		}
		seconds := end - start
		if cpus > math.MaxInt64/seconds {
			return cluster.CoreHourSummary{}, errors.New("core-hour allocation overflow")
		}
		coreSeconds := cpus * seconds
		if summary.CoreSeconds > math.MaxInt64-coreSeconds {
			return cluster.CoreHourSummary{}, errors.New("core-hour total overflow")
		}
		summary.CoreSeconds += coreSeconds
		summary.AllocationCount++
		addCoreHourGroup(accounts, job.Account, coreSeconds)
		addCoreHourGroup(users, job.User, coreSeconds)
	}
	summary.Accounts = sortedCoreHourGroups(accounts)
	summary.Users = sortedCoreHourGroups(users)
	return summary, nil
}

func allocatedCPUs(values []struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
}) (int64, error) {
	var result int64
	for _, value := range values {
		if !strings.EqualFold(value.Type, "cpu") {
			continue
		}
		if value.Count < 0 {
			return 0, errors.New("allocated CPU count must not be negative")
		}
		if result > math.MaxInt64-value.Count {
			return 0, errors.New("allocated CPU count overflow")
		}
		result += value.Count
	}
	return result, nil
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func addCoreHourGroup(groups map[string]cluster.CoreHourGroup, name string, coreSeconds int64) {
	if name == "" {
		name = "—"
	}
	group := groups[name]
	group.Name = name
	group.CoreSeconds += coreSeconds
	group.AllocationCount++
	groups[name] = group
}

func sortedCoreHourGroups(groups map[string]cluster.CoreHourGroup) []cluster.CoreHourGroup {
	result := make([]cluster.CoreHourGroup, 0, len(groups))
	for _, group := range groups {
		result = append(result, group)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CoreSeconds == result[j].CoreSeconds {
			return result[i].Name < result[j].Name
		}
		return result[i].CoreSeconds > result[j].CoreSeconds
	})
	return result
}

func cloneCoreHourSummary(value cluster.CoreHourSummary) cluster.CoreHourSummary {
	value.Accounts = append([]cluster.CoreHourGroup(nil), value.Accounts...)
	value.Users = append([]cluster.CoreHourGroup(nil), value.Users...)
	return value
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
