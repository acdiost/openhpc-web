package slurm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

	sinfo, err := c.run(ctx, "sinfo", "--noheader", "--Node", "--format=%N|%T|%C")
	if err != nil {
		return cluster.Metrics{}, fmt.Errorf("read sinfo snapshot: %w", err)
	}
	metrics, err := parseSinfo(sinfo)
	if err != nil {
		return cluster.Metrics{}, fmt.Errorf("parse sinfo snapshot: %w", err)
	}

	squeue, err := c.run(ctx, "squeue", "--noheader", "--format=%T")
	if err != nil {
		return cluster.Metrics{}, fmt.Errorf("read squeue snapshot: %w", err)
	}
	metrics.RunningJobs, metrics.QueuedJobs, err = parseSqueue(squeue)
	if err != nil {
		return cluster.Metrics{}, fmt.Errorf("parse squeue snapshot: %w", err)
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

type nodeMetrics struct {
	state     string
	allocated int
	total     int
	online    bool
}

func parseSinfo(output []byte) (cluster.Metrics, error) {
	nodes := make(map[string]nodeMetrics)
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 3 {
			return cluster.Metrics{}, fmt.Errorf("expected 3 sinfo columns, got %d", len(parts))
		}
		name := strings.TrimSpace(parts[0])
		state := normalizeNodeState(parts[1])
		if name == "" || state == "" {
			return cluster.Metrics{}, errors.New("sinfo node name and state are required")
		}
		allocated, total, err := parseCPUCounts(parts[2])
		if err != nil {
			return cluster.Metrics{}, fmt.Errorf("node %s CPU counts: %w", name, err)
		}
		hasUnavailableMarker := strings.ContainsAny(strings.TrimSpace(parts[1]), "*~#%!")
		node := nodeMetrics{state: state, allocated: allocated, total: total, online: !hasUnavailableMarker && isOnlineState(state)}
		if existing, found := nodes[name]; found {
			if existing != node {
				return cluster.Metrics{}, fmt.Errorf("node %s has conflicting sinfo records", name)
			}
			continue
		}
		nodes[name] = node
	}
	if err := scanner.Err(); err != nil {
		return cluster.Metrics{}, fmt.Errorf("scan sinfo output: %w", err)
	}

	metrics := cluster.Metrics{}
	totalOnlineCPUs := 0
	allocatedOnlineCPUs := 0
	for _, node := range nodes {
		if !node.online {
			continue
		}
		metrics.OnlineNodes++
		totalOnlineCPUs += node.total
		allocatedOnlineCPUs += node.allocated
	}
	if totalOnlineCPUs > 0 {
		metrics.CPUUsage = (allocatedOnlineCPUs*100 + totalOnlineCPUs/2) / totalOnlineCPUs
	}
	return metrics, nil
}

func parseCPUCounts(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 4 {
		return 0, 0, errors.New("expected allocated/idle/other/total")
	}
	counts := make([]int, len(parts))
	for index, part := range parts {
		count, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || count < 0 {
			return 0, 0, errors.New("CPU counts must be non-negative integers")
		}
		counts[index] = count
	}
	if counts[3] <= 0 || counts[0] > counts[3] {
		return 0, 0, errors.New("allocated CPUs must not exceed positive total CPUs")
	}
	return counts[0], counts[3], nil
}

func normalizeNodeState(value string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(value), "*~#+%!"))
}

func isOnlineState(state string) bool {
	for _, prefix := range []string{"down", "fail", "unknown", "future", "power_down", "powering_down"} {
		if strings.HasPrefix(state, prefix) {
			return false
		}
	}
	return true
}

func parseSqueue(output []byte) (running, queued int, err error) {
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		switch strings.ToUpper(strings.TrimSpace(scanner.Text())) {
		case "R", "RUNNING":
			running++
		case "PD", "PENDING":
			queued++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("scan squeue output: %w", err)
	}
	return running, queued, nil
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
