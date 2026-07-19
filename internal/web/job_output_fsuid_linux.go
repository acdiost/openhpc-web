//go:build linux

package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func readJobOutputAsUser(ctx context.Context, userID, groupID int64, path string) ([]byte, bool, error) {
	if !validJobOutputUserID(userID) {
		return nil, false, errJobOutputUnavailable
	}
	if int64(os.Geteuid()) == userID {
		return readJobOutputAtCurrentIdentity(ctx, path)
	}
	if !validJobOutputGroupID(groupID) {
		return nil, false, errJobOutputUnavailable
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, false, errJobOutputUnavailable
	}
	output := boundedJobOutputResponse{limit: int(maxJobOutputBytes) + 1}
	diagnostics := boundedJobOutputResponse{limit: 1024}
	command := exec.CommandContext(ctx, executable, jobOutputReaderArgument, strconv.FormatInt(userID, 10), strconv.FormatInt(groupID, 10))
	command.Env = []string{}
	command.Stdin = strings.NewReader(path)
	command.Stdout = &output
	command.Stderr = &diagnostics
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(userID), Gid: uint32(groupID), Groups: []uint32{uint32(groupID)}}}
	if err := command.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, false, contextErr
		}
		if detail := strings.TrimSpace(diagnostics.String()); detail != "" {
			return nil, false, fmt.Errorf("job output reader: %s", detail)
		}
		return nil, false, fmt.Errorf("start job output reader: %w", err)
	}
	if output.exceeded || output.Len() == 0 {
		return nil, false, errJobOutputUnavailable
	}
	encoded := output.Bytes()
	truncated := encoded[0] == 1
	if encoded[0] != 0 && !truncated {
		return nil, false, errJobOutputUnavailable
	}
	return append([]byte(nil), encoded[1:]...), truncated, nil
}

type boundedJobOutputResponse struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (response *boundedJobOutputResponse) Write(content []byte) (int, error) {
	originalLength := len(content)
	if len(content) > response.limit-response.Len() {
		response.exceeded = true
		content = content[:max(response.limit-response.Len(), 0)]
	}
	_, err := response.Buffer.Write(content)
	return originalLength, err
}

func validJobOutputUserID(userID int64) bool {
	return userID > 0 && userID <= int64(^uint32(0))
}

func validJobOutputGroupID(groupID int64) bool {
	return groupID > 0 && groupID <= int64(^uint32(0))
}

func runJobOutputReader(arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) != 3 {
		return errors.New("invalid reader invocation")
	}
	userID, err := strconv.ParseInt(arguments[1], 10, 64)
	groupID, groupErr := strconv.ParseInt(arguments[2], 10, 64)
	if err != nil || groupErr != nil || !validJobOutputUserID(userID) || !validJobOutputGroupID(groupID) || int64(os.Geteuid()) != userID || int64(os.Getegid()) != groupID {
		return errors.New("reader identity mismatch")
	}
	path, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if err != nil || len(path) > 1<<20 {
		return errors.New("invalid reader input")
	}
	if len(path) == 0 || strings.IndexByte(string(path), 0) >= 0 {
		return errors.New("invalid output path")
	}
	if !validJobOutputPath(string(path)) {
		return errors.New("invalid output path")
	}
	content, truncated, err := readJobOutputAtCurrentIdentity(context.Background(), string(path))
	if err != nil {
		return err
	}
	prefix := byte(0)
	if truncated {
		prefix = 1
	}
	if _, err := output.Write(append([]byte{prefix}, content...)); err != nil {
		return err
	}
	return nil
}
