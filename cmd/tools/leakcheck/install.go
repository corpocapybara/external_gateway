package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// hookMarker marks the .local wrappers this command owns; a .local without it is a foreign hook (design D10).
const hookMarker = "# leakcheck-hook: managed by cmd/tools/leakcheck install (EG0047)"

// hookScripts are the wrappers the todo tool's managed hooks run first (design D3), by file name; %s is the
// exe path.
var hookScripts = map[string]string{
	"pre-commit.local": "#!/bin/sh\n" + hookMarker + "\nexec \"%s\" pre-commit \"$@\"\n",
	"commit-msg.local": "#!/bin/sh\n" + hookMarker + "\nexec \"%s\" commit-msg \"$1\"\n",
}

func runInstall(dir string, stdout, stderr io.Writer) int {
	// `git config --get` exits 1 when the key is unset; only a set value matters here.
	if out, err := git(dir, "config", "--get", "core.hooksPath"); err == nil && strings.TrimSpace(string(out)) != "" {
		return fail(stderr, fmt.Errorf("core.hooksPath is set (%s); install writes only to the shared .git/hooks",
			strings.TrimSpace(string(out))))
	}
	common, err := gitCommonDir(dir)
	if err != nil {
		return fail(stderr, err)
	}
	hooks := filepath.Join(common, "hooks")
	exe := filepath.Join(hooks, "leakcheck.exe")
	slash := filepath.ToSlash(exe)
	if strings.ContainsAny(slash, "\"$`") {
		return fail(stderr, fmt.Errorf("hooks path %s contains a character sh would expand", slash))
	}
	for name := range hookScripts {
		p := filepath.Join(hooks, name)
		data, err := os.ReadFile(p)
		if err == nil && !strings.Contains(string(data), hookMarker) {
			return fail(stderr, fmt.Errorf("%s is a foreign hook (no leakcheck marker); move it away first", p))
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fail(stderr, err)
		}
	}
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return fail(stderr, err)
	}
	if err := installBinary(exe); err != nil {
		return fail(stderr, err)
	}
	for name, script := range hookScripts {
		p := filepath.Join(hooks, name)
		if err := os.WriteFile(p, []byte(fmt.Sprintf(script, slash)), 0o755); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "leakcheck: wrote %s\n", filepath.ToSlash(p))
	}
	fmt.Fprintf(stdout, "leakcheck: installed %s\n", slash)
	return 0
}

// installBinary copies the running executable to exe through a temp file and a rename; a copy onto itself
// is skipped.
func installBinary(exe string) error {
	src, err := os.Executable()
	if err != nil {
		return err
	}
	if si, err := os.Stat(src); err == nil {
		if di, err := os.Stat(exe); err == nil && os.SameFile(si, di) {
			return nil
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := exe + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
