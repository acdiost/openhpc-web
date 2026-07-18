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
	"github.com/openhpc-web/openhpc-web/internal/directory"
	"github.com/openhpc-web/openhpc-web/internal/ldapdirectory"
	"github.com/openhpc-web/openhpc-web/internal/slurm"
	"github.com/openhpc-web/openhpc-web/internal/web"
)

func main() {
	if warning := runtimeUserWarning(os.Geteuid()); warning != "" {
		log.Print(warning)
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
	stateWarnings, err := prepareStateDirectory(databasePath)
	if err != nil {
		log.Fatalf("prepare state directory: %v", err)
	}
	for _, warning := range stateWarnings {
		log.Print(warning)
	}
	slurmEnabled, slurmConfig, err := parseSlurmConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	slurmConfig.Warning = func(message string) { log.Print(message) }
	ldapEnabled, ldapConfig, err := parseLDAPConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	var metricsProvider cluster.Provider
	var nodeProvider cluster.NodeProvider
	var partitionProvider cluster.PartitionProvider
	var jobProvider cluster.JobProvider
	var jobResourceProvider cluster.JobResourceProvider
	var accountingProvider cluster.AccountingProvider
	var associationProvider cluster.AssociationProvider
	var coreHourProvider cluster.CoreHourProvider
	var directoryProvider directory.Provider
	if slurmEnabled {
		client, clientErr := slurm.New(slurmConfig)
		err = clientErr
		if err != nil {
			log.Fatalf("initialize Slurm integration: %v", err)
		}
		metricsProvider, nodeProvider, partitionProvider, jobProvider, jobResourceProvider, accountingProvider, associationProvider, coreHourProvider = client, client, client, client, client, client, client, client
	}
	if ldapEnabled {
		directoryProvider, err = ldapdirectory.New(ldapConfig)
		if err != nil {
			log.Fatalf("initialize LDAP integration: %v", err)
		}
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
		Warning:             func(message string) { log.Print(message) },
		AccountingProvider:  accountingProvider,
		AssociationProvider: associationProvider,
		CoreHourProvider:    coreHourProvider,
		DirectoryProvider:   directoryProvider,
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

func runtimeUserWarning(euid int) string {
	if euid != 0 {
		return ""
	}
	return "WARNING: OpenHPC Web is running as root; the application does not drop privileges or enforce owner/UID authorization. Slurm subprocesses and file reads use the running user's operating-system permissions."
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

func parseLDAPConfigFromEnv() (bool, ldapdirectory.Config, error) {
	if os.Getenv("OPENHPC_LDAP_ENABLED") != "true" {
		return false, ldapdirectory.Config{}, nil
	}
	endpoint := strings.TrimSpace(os.Getenv("OPENHPC_LDAP_URL"))
	baseDN := strings.TrimSpace(os.Getenv("OPENHPC_LDAP_BASE_DN"))
	if endpoint == "" || baseDN == "" {
		return false, ldapdirectory.Config{}, errors.New("OPENHPC_LDAP_URL and OPENHPC_LDAP_BASE_DN are required")
	}
	timeout, err := time.ParseDuration(envOr("OPENHPC_LDAP_TIMEOUT", "3s"))
	if err != nil {
		return false, ldapdirectory.Config{}, fmt.Errorf("parse OPENHPC_LDAP_TIMEOUT: %w", err)
	}
	maxResults, err := strconv.Atoi(envOr("OPENHPC_LDAP_MAX_RESULTS", "200"))
	if err != nil {
		return false, ldapdirectory.Config{}, fmt.Errorf("parse OPENHPC_LDAP_MAX_RESULTS: %w", err)
	}
	config := ldapdirectory.Config{
		URL: endpoint, BaseDN: baseDN,
		UserBaseDN: strings.TrimSpace(os.Getenv("OPENHPC_LDAP_USER_BASE_DN")), GroupBaseDN: strings.TrimSpace(os.Getenv("OPENHPC_LDAP_GROUP_BASE_DN")),
		BindDN: strings.TrimSpace(os.Getenv("OPENHPC_LDAP_BIND_DN")), BindPassword: os.Getenv("OPENHPC_LDAP_BIND_PASSWORD"),
		CAFile: strings.TrimSpace(os.Getenv("OPENHPC_LDAP_CA_FILE")), Timeout: timeout, MaxResults: maxResults,
	}
	if err := ldapdirectory.ValidateConfig(config); err != nil {
		return false, ldapdirectory.Config{}, fmt.Errorf("validate LDAP configuration: %w", err)
	}
	return true, config, nil
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

func prepareStateDirectory(databasePath string) ([]string, error) {
	if databasePath == ":memory:" {
		return nil, nil
	}
	directory := filepath.Dir(databasePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", directory, err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", directory, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a real directory", directory)
	}
	return stateDirectoryWarnings(info, os.Geteuid()), nil
}

func stateDirectoryWarnings(info os.FileInfo, effectiveUID int) []string {
	warnings := make([]string, 0, 2)
	if info.Mode().Perm()&0o077 != 0 {
		warnings = append(warnings, "WARNING: state directory permissions are broader than 0700; relying on the running user's operating-system permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != effectiveUID {
		warnings = append(warnings, "WARNING: state directory owner differs from the running user; relying on the running user's operating-system permissions")
	}
	return warnings
}
