package slurmconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	maxConfigBytesHard = 4 << 20
	maxConfigFiles     = 128
)

type File struct {
	Name      string
	Size      int64
	Content   string
	Truncated bool
}

type Entry struct {
	Name string
	Size int64
}

type Provider interface {
	List(context.Context) ([]Entry, error)
	Read(context.Context, string) (File, error)
}

type LocalProvider struct {
	root     string
	rootFD   int
	maxBytes int64
}

func New(root string, maxBytes int64) (*LocalProvider, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return nil, errors.New("Slurm config root must be a clean absolute directory")
	}
	if maxBytes <= 0 || maxBytes > maxConfigBytesHard {
		return nil, errors.New("Slurm config size limit is invalid")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect Slurm config root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Slurm config root must be a real directory")
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open Slurm config root: %w", err)
	}
	return &LocalProvider{root: root, rootFD: fd, maxBytes: maxBytes}, nil
}

func (p *LocalProvider) Close() error {
	if p == nil || p.rootFD < 0 {
		return nil
	}
	err := unix.Close(p.rootFD)
	p.rootFD = -1
	return err
}

func (p *LocalProvider) List(ctx context.Context) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dup, err := unix.Dup(p.rootFD)
	if err != nil {
		return nil, fmt.Errorf("duplicate Slurm config root: %w", err)
	}
	directory := os.NewFile(uintptr(dup), p.root)
	defer directory.Close()
	entries, err := directory.Readdir(-1)
	if err != nil {
		return nil, err
	}
	result := make([]Entry, 0, len(entries))
	for _, info := range entries {
		if len(result) >= maxConfigFiles || !validConfigName(info.Name()) || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		result = append(result, Entry{Name: info.Name(), Size: info.Size()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func selectConfigEntries(entries []fs.DirEntry) []Entry {
	result := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if len(result) >= maxConfigFiles || !validConfigName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		result = append(result, Entry{Name: entry.Name(), Size: info.Size()})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (p *LocalProvider) Read(ctx context.Context, name string) (File, error) {
	if err := ctx.Err(); err != nil {
		return File{}, err
	}
	if !validConfigName(name) {
		return File{}, errors.New("invalid Slurm config file name")
	}
	fd, err := unix.Openat(p.rootFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return File{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return File{}, errors.New("Slurm config entry is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, p.maxBytes+1))
	if err != nil {
		return File{}, err
	}
	truncated := int64(len(content)) > p.maxBytes
	if truncated {
		content = content[:p.maxBytes]
	}
	if !utf8.Valid(content) {
		content = []byte(strings.ToValidUTF8(string(content), "�"))
	}
	return File{Name: name, Size: info.Size(), Content: redact(string(content)), Truncated: truncated}, nil
}

func validConfigName(name string) bool {
	if name == "" || len(name) > 128 || name == "." || name == ".." || strings.ContainsAny(name, `/\\\x00`) {
		return false
	}
	for _, char := range name {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func ValidName(name string) bool { return validConfigName(name) }

func redact(content string) string {
	lines := strings.Split(content, "\n")
	for index, line := range lines {
		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:separator]))
		for _, marker := range []string{"password", "pass", "token", "secret", "privatekey", "authinfo", "jwt"} {
			if strings.Contains(key, marker) {
				lines[index] = line[:separator+1] + "REDACTED"
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}
