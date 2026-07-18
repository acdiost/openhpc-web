package slurm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
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
	nodesCache     valueCache[[]cluster.Node]
	jobsCache      valueCache[[]cluster.Job]
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
		for _, command := range []string{"sinfo", "squeue"} {
			path := filepath.Join(config.BinaryDir, command)
			if err := validateRootOwnedExecutable(path); err != nil {
				return nil, err
			}
		}
		runner = &CommandRunner{MaxOutputBytes: config.MaxOutputBytes, Environment: allowedSlurmEnvironment(os.Environ())}
	}
	return &Client{
		binaryDir: config.BinaryDir, timeout: config.Timeout,
		maxOutputBytes: config.MaxOutputBytes, runner: runner, cacheTTL: config.CacheTTL,
		nodesCache: valueCache[[]cluster.Node]{ttl: config.CacheTTL},
		jobsCache:  valueCache[[]cluster.Job]{ttl: config.CacheTTL},
		now:        time.Now,
	}, nil
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

func validateRootOwnedExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Slurm command %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("Slurm command %s must be a non-symlink executable file", path)
	}
	for current := path; ; current = filepath.Dir(current) {
		entry, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect Slurm path %s: %w", current, err)
		}
		stat, ok := entry.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || entry.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("Slurm path %s must be root-owned and not group/world writable", current)
		}
		if current == "/" {
			break
		}
	}
	return nil
}
