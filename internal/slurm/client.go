package slurm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type Config struct {
	BinaryDir      string
	Timeout        time.Duration
	MaxOutputBytes int
	CacheTTL       time.Duration
	Runner         Runner
	Warning        func(string)
}

type Client struct {
	binaryDir      string
	timeout        time.Duration
	maxOutputBytes int
	runner         Runner
	cacheTTL       time.Duration
	cacheMu        sync.Mutex
	lastAttempt    time.Time
	lastMetrics    cluster.Metrics
	lastErr        error
	refreshing     chan struct{}
	nodesCache     valueCache[nodeSnapshot]
	jobsCache      valueCache[[]cluster.Job]
	accountCache   valueCache[accountingSnapshot]
	qosCache       valueCache[[]cluster.QoS]
	coreHourCaches [3]valueCache[cluster.CoreHourSummary]
	now            func() time.Time
}

const (
	maxCommandTimeout = 30 * time.Second
	maxOutputBytes    = 8 << 20
	defaultCacheTTL   = 10 * time.Second
	maxCacheTTL       = time.Minute
)

func New(config Config) (*Client, error) {
	if !filepath.IsAbs(config.BinaryDir) || filepath.Clean(config.BinaryDir) != config.BinaryDir {
		return nil, errors.New("Slurm binary directory must be an absolute clean path")
	}
	if config.Timeout <= 0 || config.Timeout > maxCommandTimeout {
		return nil, errors.New("Slurm command timeout must be positive")
	}
	if config.MaxOutputBytes <= 0 || config.MaxOutputBytes > maxOutputBytes {
		return nil, errors.New("Slurm output limit must be positive")
	}
	if config.CacheTTL == 0 {
		config.CacheTTL = defaultCacheTTL
	}
	if config.CacheTTL < 0 || config.CacheTTL > maxCacheTTL {
		return nil, errors.New("Slurm cache TTL must be between zero and one minute")
	}
	runner := config.Runner
	if runner == nil {
		warning := config.Warning
		if warning == nil {
			warning = func(message string) { log.Print(message) }
		}
		reportedWarnings := make(map[string]struct{})
		for _, command := range []string{"sinfo", "squeue", "sacct", "sacctmgr", "sstat", "scontrol"} {
			path := filepath.Join(config.BinaryDir, command)
			warnings, err := validateSlurmExecutable(path)
			if err != nil {
				return nil, err
			}
			for _, message := range warnings {
				if _, reported := reportedWarnings[message]; reported {
					continue
				}
				reportedWarnings[message] = struct{}{}
				warning(message)
			}
		}
		runner = &CommandRunner{MaxOutputBytes: config.MaxOutputBytes, Environment: allowedSlurmEnvironment(os.Environ())}
	}
	client := &Client{
		binaryDir: config.BinaryDir, timeout: config.Timeout,
		maxOutputBytes: config.MaxOutputBytes, runner: runner, cacheTTL: config.CacheTTL,
		nodesCache:   valueCache[nodeSnapshot]{ttl: config.CacheTTL},
		jobsCache:    valueCache[[]cluster.Job]{ttl: config.CacheTTL},
		accountCache: valueCache[accountingSnapshot]{ttl: config.CacheTTL},
		qosCache:     valueCache[[]cluster.QoS]{ttl: config.CacheTTL},
		now:          time.Now,
	}
	for index := range client.coreHourCaches {
		client.coreHourCaches[index].ttl = config.CacheTTL
	}
	return client, nil
}

func (c *Client) Snapshot(parent context.Context) (cluster.Metrics, error) {
	for {
		c.cacheMu.Lock()
		if !c.lastAttempt.IsZero() && time.Since(c.lastAttempt) < c.cacheTTL {
			metrics, err := c.lastMetrics, c.lastErr
			c.cacheMu.Unlock()
			return metrics, err
		}
		if refresh := c.refreshing; refresh != nil {
			c.cacheMu.Unlock()
			select {
			case <-parent.Done():
				return cluster.Metrics{}, parent.Err()
			case <-refresh:
				continue
			}
		}
		refresh := make(chan struct{})
		c.refreshing = refresh
		c.cacheMu.Unlock()

		metrics, err := c.snapshot(parent)
		c.cacheMu.Lock()
		if parent.Err() == nil {
			c.lastAttempt, c.lastMetrics, c.lastErr = time.Now(), metrics, err
		}
		c.refreshing = nil
		close(refresh)
		c.cacheMu.Unlock()
		if parent.Err() != nil {
			return cluster.Metrics{}, parent.Err()
		}
		return metrics, err
	}
}

func (c *Client) snapshot(parent context.Context) (cluster.Metrics, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()

	nodes, err := c.Nodes(ctx)
	if err != nil {
		return cluster.Metrics{}, fmt.Errorf("read node snapshot: %w", err)
	}
	jobs, err := c.Jobs(ctx)
	if err != nil {
		return cluster.Metrics{}, fmt.Errorf("read job snapshot: %w", err)
	}
	return aggregateMetrics(nodes, jobs)
}

func aggregateMetrics(nodes []cluster.Node, jobs []cluster.Job) (cluster.Metrics, error) {
	uniqueNodes := make(map[string]cluster.Node, len(nodes))
	for _, node := range nodes {
		if existing, found := uniqueNodes[node.Name]; found {
			if existing.State != node.State || existing.AllocatedCPUs != node.AllocatedCPUs || existing.TotalCPUs != node.TotalCPUs || existing.Online != node.Online {
				return cluster.Metrics{}, fmt.Errorf("node %s has conflicting JSON records", node.Name)
			}
			continue
		}
		uniqueNodes[node.Name] = node
	}
	metrics := cluster.Metrics{}
	totalOnlineCPUs := 0
	allocatedOnlineCPUs := 0
	for _, node := range uniqueNodes {
		if !node.Online {
			continue
		}
		metrics.OnlineNodes++
		totalOnlineCPUs += node.TotalCPUs
		allocatedOnlineCPUs += node.AllocatedCPUs
	}
	if totalOnlineCPUs > 0 {
		metrics.CPUUsage = (allocatedOnlineCPUs*100 + totalOnlineCPUs/2) / totalOnlineCPUs
	}
	for _, job := range jobs {
		switch strings.ToUpper(job.State) {
		case "RUNNING":
			metrics.RunningJobs++
		case "PENDING":
			metrics.QueuedJobs++
		}
	}
	return metrics, nil
}

func (c *Client) run(ctx context.Context, command string, args ...string) ([]byte, error) {
	output, err := c.runner.Run(ctx, filepath.Join(c.binaryDir, command), args...)
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", command, err)
	}
	if len(output) > c.maxOutputBytes {
		return nil, fmt.Errorf("run %s: %w", command, ErrOutputLimit)
	}
	return output, nil
}

func allowedSlurmEnvironment(environment []string) []string {
	allowed := map[string]bool{"SLURM_CONF": true, "SLURM_CONF_SERVER": true, "SLURM_CLUSTERS": true, "MUNGE_SOCKET": true}
	result := make([]string, 0, len(allowed))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && allowed[name] {
			result = append(result, entry)
		}
	}
	return result
}

func validateSlurmExecutable(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect Slurm command %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("Slurm command %s must be a non-symlink executable file", path)
	}
	warnings := make([]string, 0)
	for current := path; ; current = filepath.Dir(current) {
		entry, err := os.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("inspect Slurm path %s: %w", current, err)
		}
		stat, ok := entry.Sys().(*syscall.Stat_t)
		ownerUID := ^uint32(0)
		if ok {
			ownerUID = stat.Uid
		}
		warnings = append(warnings, slurmPathRiskWarnings(current, ownerUID, entry.Mode().Perm())...)
		if current == "/" {
			break
		}
	}
	return warnings, nil
}

func slurmPathRiskWarnings(path string, ownerUID uint32, mode os.FileMode) []string {
	warnings := make([]string, 0, 2)
	if ownerUID != 0 {
		warnings = append(warnings, fmt.Sprintf("WARNING: Slurm path %s owner is not root; relying on the running user's operating-system permissions", path))
	}
	if mode&0o022 != 0 {
		warnings = append(warnings, fmt.Sprintf("WARNING: Slurm path %s is group/world writable; relying on the running user's operating-system permissions", path))
	}
	return warnings
}
