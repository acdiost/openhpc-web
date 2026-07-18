package slurm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

var ErrOutputLimit = errors.New("Slurm command output exceeded configured limit")

type CommandRunner struct {
	MaxOutputBytes int
	Environment    []string
}

func (r *CommandRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if r.MaxOutputBytes <= 0 {
		return nil, errors.New("command output limit must be positive")
	}
	commandContext, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &limitedBuffer{limit: r.MaxOutputBytes, cancel: cancel}
	command := exec.CommandContext(commandContext, path, args...)
	command.Env = append(cleanEnvironment(r.Environment), "LC_ALL=C", "LANG=C")
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if output.exceededLimit() {
		return nil, ErrOutputLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("command failed: %w", err)
	}
	return output.bytes(), nil
}

type limitedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		writeLength := len(value)
		if writeLength > remaining {
			writeLength = remaining
		}
		_, _ = b.buffer.Write(value[:writeLength])
	}
	if len(value) > remaining {
		b.exceeded = true
		if b.cancel != nil {
			b.cancel()
		}
	}
	return len(value), nil
}

func cleanEnvironment(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found && name != "LC_ALL" && name != "LANG" && !strings.HasPrefix(name, "OPENHPC_") {
			result = append(result, entry)
		}
	}
	return result
}

func (b *limitedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}

func (b *limitedBuffer) exceededLimit() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}
