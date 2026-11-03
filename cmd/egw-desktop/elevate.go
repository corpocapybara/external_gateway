//go:build windows

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/external_gateway/deploy"
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
		args, err := deployArgs(a.buildDir, a.installDir)
		if err != nil {
			return fail(err)
		}
		out, err := execPowerShell(args)
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

// execPowerShell runs powershell.exe with args; a variable so tests can intercept it.
var execPowerShell = func(args []string) ([]byte, error) {
	cmd := exec.Command("powershell.exe", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.CombinedOutput()
}

// deployArgs builds the powershell command line for the elevated deploy. The script text comes from
// the deploy package (embedded in this executable at build time), never from the checkout, and is
// passed inline via -EncodedCommand, so no file exists that could be swapped between check and use
// (no TOCTOU, no temp file in a user-writable directory). The checkout is passed only as data
// (-BuildDir); see deployScript for the inputs that are still read from it.
func deployArgs(buildDir, installDir string) ([]string, error) {
	text := deployScript(buildDir, installDir)
	units := utf16.Encode([]rune(text))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		raw[2*i], raw[2*i+1] = byte(u), byte(u>>8)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	if len(enc) > 30000 { // CreateProcess command line limit is 32767 chars
		return nil, fmt.Errorf("embedded deploy script too large for -EncodedCommand (%d chars)", len(enc))
	}
	return []string{"-NoProfile", "-NonInteractive", "-OutputFormat", "Text", "-ExecutionPolicy", "Bypass", "-EncodedCommand", enc}, nil
}

// psQuote returns a PowerShell expression yielding s. It is base64 so no character in s (including the
// typographic quotes U+2018..U+201B that PowerShell treats as single quotes) can end the literal.
func psQuote(s string) string {
	return "([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" +
		base64.StdEncoding.EncodeToString([]byte(s)) + "')))"
}

// deployScript composes the embedded workspaces.ps1 and update.ps1 into one script. Inputs that are
// still read from buildDir at run time and are NOT pinned: config.<ws>.yaml / operations.<ws>.yaml
// (copied into Program Files) and egw.exe (copied to Program Files and run as LocalSystem).
func deployScript(buildDir, installDir string) string {
	return deploy.WorkspacesScript + "\n$egwDeploy = {\n" + deploy.UpdateScript + "\n}\n& $egwDeploy -BuildDir " +
		psQuote(buildDir) + " -InstallDir " + psQuote(installDir) + "\n"
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
