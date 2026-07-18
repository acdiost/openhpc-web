package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
	"github.com/openhpc-web/openhpc-web/internal/slurm"
	"github.com/openhpc-web/openhpc-web/internal/web"
)

func main() {
	if os.Geteuid() == 0 {
		log.Fatal("OpenHPC Web must not run as root; use a least-privilege service account")
	}
	username := os.Getenv("OPENHPC_ADMIN_USERNAME")
	password := os.Getenv("OPENHPC_ADMIN_PASSWORD")
	if username == "" || password == "" {
		log.Fatal("OPENHPC_ADMIN_USERNAME and OPENHPC_ADMIN_PASSWORD are required")
	}

	address := envOr("OPENHPC_ADDRESS", "127.0.0.1:8080")
	secureCookies := os.Getenv("OPENHPC_SECURE_COOKIES") == "true"
	trustedProxyCIDRs := splitList(os.Getenv("OPENHPC_TRUSTED_PROXY_CIDRS"))
	jobOutputRoots := splitList(os.Getenv("OPENHPC_JOB_OUTPUT_ROOTS"))
	if err := validateDeploymentConfig(address, secureCookies, trustedProxyCIDRs); err != nil {
		log.Fatal(err)
	}
	databasePath := envOr("OPENHPC_DATABASE_PATH", filepath.Join("state", "openhpc.db"))
	if err := prepareStateDirectory(databasePath); err != nil {
		log.Fatalf("prepare state directory: %v", err)
	}
	slurmEnabled, slurmConfig, err := parseSlurmConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	var metricsProvider cluster.Provider
	var nodeProvider cluster.NodeProvider
	var partitionProvider cluster.PartitionProvider
	var jobProvider cluster.JobProvider
	var jobResourceProvider cluster.JobResourceProvider
	var accountingProvider cluster.AccountingProvider
	if slurmEnabled {
		client, clientErr := slurm.New(slurmConfig)
		err = clientErr
		if err != nil {
			log.Fatalf("initialize Slurm integration: %v", err)
		}
		metricsProvider, nodeProvider, partitionProvider, jobProvider, jobResourceProvider, accountingProvider = client, client, client, client, client, client
	}
	handler, err := web.New(web.Config{
		AdminUsername:       username,
		AdminPassword:       password,
		DatabasePath:        databasePath,
		SecureCookies:       secureCookies,
		TrustedProxyCIDRs:   trustedProxyCIDRs,
		MetricsProvider:     metricsProvider,
		NodeProvider:        nodeProvider,
		PartitionProvider:   partitionProvider,
		JobProvider:         jobProvider,
		JobResourceProvider: jobResourceProvider,
		JobOutputRoots:      jobOutputRoots,
		AccountingProvider:  accountingProvider,
	})
	if err != nil {
		log.Fatalf("initialize server: %v", err)
	}
	if closer, ok := handler.(interface{ Close() error }); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				log.Printf("close application: %v", err)
			}
		}()
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("OpenHPC Web listening on http://%s", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func parseSlurmConfigFromEnv() (bool, slurm.Config, error) {
	if os.Getenv("OPENHPC_SLURM_ENABLED") != "true" {
		return false, slurm.Config{}, nil
	}
	timeoutValue := envOr("OPENHPC_SLURM_TIMEOUT", "3s")
	timeout, err := time.ParseDuration(timeoutValue)
	if err != nil {
		return false, slurm.Config{}, fmt.Errorf("parse OPENHPC_SLURM_TIMEOUT: %w", err)
	}
	maxOutputValue := envOr("OPENHPC_SLURM_MAX_OUTPUT", "2097152")
	maxOutput, err := strconv.Atoi(maxOutputValue)
	if err != nil {
		return false, slurm.Config{}, fmt.Errorf("parse OPENHPC_SLURM_MAX_OUTPUT: %w", err)
	}
	cacheTTLValue := envOr("OPENHPC_SLURM_CACHE_TTL", "10s")
	cacheTTL, err := time.ParseDuration(cacheTTLValue)
	if err != nil {
		return false, slurm.Config{}, fmt.Errorf("parse OPENHPC_SLURM_CACHE_TTL: %w", err)
	}
	return true, slurm.Config{
		BinaryDir:      envOr("OPENHPC_SLURM_BIN_DIR", "/usr/local/bin"),
		Timeout:        timeout,
		MaxOutputBytes: maxOutput,
		CacheTTL:       cacheTTL,
	}, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if normalized := strings.TrimSpace(part); normalized != "" {
			result = append(result, normalized)
		}
	}
	return result
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validateDeploymentConfig(address string, secureCookies bool, trustedProxyCIDRs []string) error {
	if !isLoopbackAddress(address) {
		return errors.New("OPENHPC_ADDRESS must be a loopback listener behind a local TLS reverse proxy")
	}
	if secureCookies && len(trustedProxyCIDRs) == 0 {
		return errors.New("OPENHPC_TRUSTED_PROXY_CIDRS is required when secure cookies are enabled")
	}
	return nil
}

func prepareStateDirectory(databasePath string) error {
	if databasePath == ":memory:" {
		return nil
	}
	directory := filepath.Dir(databasePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", directory, err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", directory, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s must be a real directory", directory)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s permissions must be 0700", directory)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s must be owned by the service account", directory)
	}
	return nil
}
