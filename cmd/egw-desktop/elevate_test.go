//go:build windows

package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/external_gateway/deploy"
)

const tamperMarker = "EVIL-TAMPERED-MARKER"

func stubExec(t *testing.T) *[]string {
	var got []string
	orig := execPowerShell
	execPowerShell = func(args []string) ([]byte, error) { got = args; return []byte("ok"), nil }
	t.Cleanup(func() { execPowerShell = orig })
	return &got
}

func decodeEncoded(t *testing.T, args []string) string {
	t.Helper()
	if len(args) < 2 || args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("expected trailing -EncodedCommand, got %v", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

// A deploy script planted in the user-writable checkout must never reach the elevated powershell.
func TestDeployIgnoresCheckoutScript(t *testing.T) {
	build := t.TempDir()
	if err := os.MkdirAll(filepath.Join(build, "deploy"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"update.ps1", "workspaces.ps1"} {
		if err := os.WriteFile(filepath.Join(build, "deploy", f), []byte("Write-Host "+tamperMarker), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got := stubExec(t)

	a := &app{buildDir: build, installDir: filepath.Join(build, "install"), elevated: true}
	res := doHelper(a, "deploy", "", "", false)
	if !res.OK {
		t.Fatalf("deploy refused: %s", res.Error)
	}
	joined := strings.Join(*got, " ")
	if strings.Contains(joined, "-File") || strings.Contains(joined, filepath.Join(build, "deploy")) {
		t.Errorf("powershell is pointed at a script in the user-writable checkout: %s", joined)
	}
	if strings.Contains(joined, tamperMarker) || strings.Contains(decodeEncoded(t, *got), tamperMarker) {
		t.Errorf("tampered script content reached powershell")
	}
}

// The command that runs is exactly the embedded script plus the quoted directories.
func TestDeployRunsEmbeddedScript(t *testing.T) {
	got := stubExec(t)
	a := &app{buildDir: `C:\it's a build`, installDir: `C:\Program Files\external_gateway`, elevated: true}
	if res := doHelper(a, "deploy", "", "", false); !res.OK {
		t.Fatalf("deploy refused: %s", res.Error)
	}
	text := decodeEncoded(t, *got)
	if !strings.Contains(text, deploy.UpdateScript) || !strings.Contains(text, deploy.WorkspacesScript) {
		t.Error("command does not contain the embedded scripts verbatim")
	}
	if !strings.Contains(text, `-BuildDir 'C:\it''s a build'`) {
		t.Errorf("build dir not quoted safely: %s", text[len(text)-120:])
	}
}

// The composed script must be valid PowerShell (parse only, nothing is executed).
func TestDeployScriptParses(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe not available")
	}
	file := filepath.Join(t.TempDir(), "composed.ps1")
	if err := os.WriteFile(file, []byte(deployScript(`C:\it's a build`, `C:\x`)), 0644); err != nil {
		t.Fatal(err)
	}
	check := `$e=$null;$t=$null;[void][Management.Automation.Language.Parser]::ParseFile($args[0],[ref]$t,[ref]$e);if($e){$e|%{$_.Message};exit 1}`
	out, err := exec.Command(ps, "-NoProfile", "-Command", "& { "+check+" }", file).CombinedOutput()
	if err != nil {
		t.Fatalf("composed deploy script does not parse: %v\n%s", err, out)
	}
}
