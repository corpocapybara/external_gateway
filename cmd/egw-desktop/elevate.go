//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type helperResult struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Output string `json:"output,omitempty"`
}

// runElevated performs an admin-only action: in-process when already elevated, otherwise through
// a one-shot copy of this executable started via the UAC prompt.
func (a *app) runElevated(action, ws, svcAction string, restart bool) (helperResult, error) {
	if a.elevated {
		return doHelper(a, action, ws, svcAction, restart), nil
	}
	resultFile := filepath.Join(os.TempDir(), "egw-desktop-"+randomToken(8)+".json")
	defer os.Remove(resultFile)
	args := []string{"-helper", action, "-result", resultFile, "-build-dir", a.buildDir, "-install-dir", a.installDir}
	if ws != "" {
		args = append(args, "-ws", ws)
	}
	if svcAction != "" {
		args = append(args, "-action", svcAction)
	}
	if restart {
		args = append(args, "-restart")
	}
	if err := relaunchElevated(args); err != nil {
		return helperResult{}, fmt.Errorf("administrator rights are needed for this (UAC prompt declined or failed: %v)", err)
	}
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(resultFile); err == nil && len(data) > 0 {
			var res helperResult
			if err := json.Unmarshal(data, &res); err == nil {
				if !res.OK {
					return res, fmt.Errorf("%s", res.Error)
				}
				return res, nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return helperResult{}, fmt.Errorf("the elevated helper did not finish within 5 minutes")
}

func runHelper(a *app, action, resultFile, ws, svcAction string, restart bool) {
	res := doHelper(a, action, ws, svcAction, restart)
	data, _ := json.Marshal(res)
	tmp := resultFile + ".tmp"
	os.WriteFile(tmp, data, 0600)
	os.Rename(tmp, resultFile)
}

var validName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func doHelper(a *app, action, ws, svcAction string, restart bool) helperResult {
	fail := func(err error) helperResult { return helperResult{Error: err.Error()} }
	if ws != "" && !validName.MatchString(ws) {
		return fail(fmt.Errorf("invalid workspace name"))
	}
	switch action {
	case "deploy":
		script := filepath.Join(a.buildDir, "deploy", "update.ps1")
		cmd := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			return helperResult{Error: fmt.Sprintf("%v\n%s", err, out), Output: string(out)}
		}
		return helperResult{OK: true, Output: string(out)}
	case "service":
		if svcAction != "start" && svcAction != "stop" && svcAction != "restart" {
			return fail(fmt.Errorf("unknown service action"))
		}
		if err := controlService("egw-"+ws, svcAction); err != nil {
			return fail(err)
		}
		return helperResult{OK: true}
	case "publish":
		if err := publishFiles(a, ws); err != nil {
			return fail(err)
		}
		if restart {
			if err := controlService("egw-"+ws, "restart"); err != nil {
				return fail(fmt.Errorf("published, but restart failed: %w", err))
			}
		}
		return helperResult{OK: true}
	}
	return fail(fmt.Errorf("unknown helper action %q", action))
}

// publishFiles copies one workspace's source config and operations into the install dir the same
// way deploy\update.ps1 does (UTF-8 without BOM, trailing whitespace trimmed).
func publishFiles(a *app, ws string) error {
	dir := filepath.Join(a.installDir, ws)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	trail := regexp.MustCompile(`(?m)[\t ]+(\r?)$`)
	for src, dst := range map[string]string{a.repoConfigPath(ws): "config.yaml", a.repoOpsPath(ws): "operations.yaml"} {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		text := strings.TrimPrefix(string(data), string(rune(0xFEFF)))
		text = trail.ReplaceAllString(text, "$1")
		if err := os.WriteFile(filepath.Join(dir, dst), []byte(text), 0644); err != nil {
			return err
		}
	}
	return nil
}
