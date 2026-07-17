package secrets

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type PassStore struct {
	Dir string
}

func (s *PassStore) resolve(workspace, name string) (string, error) {
	dir := s.Dir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no password-store dir configured and $HOME not set")
		}
		dir = filepath.Join(home, ".password-store")
	}
	f := filepath.Join(dir, "egw", workspace, name+".gpg")
	if _, err := os.Stat(f); err != nil {
		return "", fmt.Errorf("pass entry not found: egw/%s/%s", workspace, name)
	}
	cmd := exec.Command("gpg", "--decrypt", "--batch", "--quiet", f)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gpg decrypt failed for %s: %w", f, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *PassStore) Resolve(workspace, name string) ([]byte, error) {
	val, err := s.resolve(workspace, name)
	if err != nil {
		return nil, err
	}
	return []byte(val), nil
}

func (s *PassStore) Set(workspace, name string, value []byte) error {
	dir := s.Dir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("no password-store dir configured and $HOME not set")
		}
		dir = filepath.Join(home, ".password-store")
	}
	targetDir := filepath.Join(dir, "egw", workspace)
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return fmt.Errorf("creating pass directory: %w", err)
	}
	// Pipe value to gpg --encrypt
	cmd := exec.Command("gpg", "--encrypt", "--batch", "--yes",
		"--recipient", "self",
		"--output", filepath.Join(targetDir, name+".gpg"))
	cmd.Stdin = strings.NewReader(string(value))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gpg encrypt failed for egw/%s/%s: %w (output: %s)", workspace, name, err, string(out))
	}
	return nil
}

func (s *PassStore) Delete(workspace, name string) error {
	dir := s.Dir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("no password-store dir configured and $HOME not set")
		}
		dir = filepath.Join(home, ".password-store")
	}
	f := filepath.Join(dir, "egw", workspace, name+".gpg")
	return os.Remove(f)
}
