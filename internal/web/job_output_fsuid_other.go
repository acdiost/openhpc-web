//go:build !linux

package web

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func readJobOutputAsUser(ctx context.Context, userID, _ int64, path string) ([]byte, bool, error) {
	if userID <= 0 || int64(os.Geteuid()) != userID {
		return nil, false, errJobOutputUnavailable
	}
	return readJobOutputAtCurrentIdentity(ctx, resolveSystemPathAlias(path))
}

func resolveSystemPathAlias(path string) string {
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "darwin" && (cleaned == "/var" || strings.HasPrefix(cleaned, "/var/")) {
		return "/private" + cleaned
	}
	return cleaned
}

func runJobOutputReader([]string, io.Reader, io.Writer) error {
	return errors.New("job output reader is supported only on Linux")
}
