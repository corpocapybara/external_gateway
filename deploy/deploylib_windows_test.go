package deploy

// Tests for deploylib.ps1, the helpers deploy\update.ps1 uses so a failed deploy (egw.exe still
// locked, a stop timeout, a config copy) never leaves the gateways stopped (EG0072). Each test runs
// the helpers in one powershell.exe; no service is touched. update.ps1 itself needs Administrator
// and stops the real gateways, so TestUpdateScriptWiring checks its use of the helpers statically.

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// psCommand returns powershell.exe running script after dot-sourcing deploylib.ps1.
func psCommand(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	lib, err := filepath.Abs("deploylib.ps1")
	if err != nil {
		t.Fatal(err)
	}
	full := "$ErrorActionPreference = 'Stop'; . '" + lib + "'; " + script
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", full)
}

func runPS(t *testing.T, script string) (string, error) {
	t.Helper()
	out, err := psCommand(t, script).CombinedOutput()
	return string(out), err
}

// outputLines returns the lines of out that start with prefix, prefix removed, line ends trimmed.
func outputLines(out, prefix string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, prefix) {
			got = append(got, strings.TrimPrefix(l, prefix))
		}
	}
	return got
}

// lockFile opens path with share mode 0, so any other open (Copy-Item's included) fails with a
// sharing violation until the returned handle is closed.
func lockFile(t *testing.T, path string) syscall.Handle {
	t.Helper()
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("lock %s: %v", path, err)
	}
	return h
}

// copyPair writes src (new bytes) and dst (old bytes) in a temp dir.
func copyPair(t *testing.T) (src, dst string) {
	dir := t.TempDir()
	src, dst = filepath.Join(dir, "new.exe"), filepath.Join(dir, "egw.exe")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return src, dst
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s holds %q, want %q", path, got, want)
	}
}

func TestCopyWithRetryLockReleased(t *testing.T) {
	t.Parallel()
	src, dst := copyPair(t)
	h := lockFile(t, dst)
	released := false
	defer func() {
		if !released {
			syscall.CloseHandle(h)
		}
	}()

	cmd := psCommand(t, "Copy-EgwFileWithRetry -Source '"+src+"' -Destination '"+dst+"' -TimeoutSeconds 30 -IntervalMilliseconds 200")
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close() // the child holds its own copy; EOF comes when it exits
	defer pr.Close()
	// Release the lock only once the helper has hit it and said it retries. If no retry line
	// comes, the helper gives up after its 30 s timeout and the checks below fail.
	var out bytes.Buffer
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		out.WriteString(sc.Text() + "\n")
		if !released && strings.Contains(sc.Text(), "locked, retrying") {
			syscall.CloseHandle(h)
			released = true
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out.String())
	}
	if !released {
		t.Fatalf("helper never printed its retry line:\n%s", out.String())
	}
	assertContent(t, dst, "new")
}

func TestCopyWithRetryLockHeld(t *testing.T) {
	t.Parallel()
	src, dst := copyPair(t)
	h := lockFile(t, dst)
	defer func() {
		if h != 0 {
			syscall.CloseHandle(h)
		}
	}()

	out, err := runPS(t, "Copy-EgwFileWithRetry -Source '"+src+"' -Destination '"+dst+"' -TimeoutSeconds 2 -IntervalMilliseconds 200")
	if err == nil {
		t.Fatalf("helper succeeded on a locked destination:\n%s", out)
	}
	if !strings.Contains(out, "locked, retrying") {
		t.Errorf("helper gave up without retrying:\n%s", out)
	}
	if !strings.Contains(out, "copy to "+dst+" still failing after 2 s") {
		t.Errorf("error does not name the destination:\n%s", out)
	}
	syscall.CloseHandle(h)
	h = 0
	assertContent(t, dst, "old")
}

// Both cases must fail at once, not wait out the 30 s lock timeout. A missing source surfaces as
// PowerShell's ItemNotFoundException (not an IOException); a missing destination folder is a
// DirectoryNotFoundException, an IOException subclass that the retry must exclude.
func TestCopyWithRetryNoRetryOnMissingPath(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, src, dst string }{
		{"missing source", "absent.exe", "egw.exe"},
		{"missing destination folder", "new.exe", filepath.Join("no-such-dir", "egw.exe")},
	} {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			src, dst := copyPair(t)
			dir := filepath.Dir(dst)
			start := time.Now()
			out, err := runPS(t, "Copy-EgwFileWithRetry -Source '"+filepath.Join(dir, c.src)+"' -Destination '"+filepath.Join(dir, c.dst)+"' -TimeoutSeconds 30")
			if err == nil {
				t.Fatalf("helper succeeded:\n%s", out)
			}
			if d := time.Since(start); d > 10*time.Second {
				t.Errorf("took %v, want under 10 s (no retry)", d)
			}
			if strings.Contains(out, "retrying") {
				t.Errorf("was retried:\n%s", out)
			}
			assertContent(t, src, "new")
			assertContent(t, dst, "old")
		})
	}
}

func TestSelectRunningServices(t *testing.T) {
	t.Parallel()
	out, err := runPS(t, `function Show($in) { 'NAMES=' + ((Select-EgwRunningServices $in) -join ',') + '|COUNT=' + @(Select-EgwRunningServices $in).Count }
Add-Type -AssemblyName System.ServiceProcess
$run = [System.ServiceProcess.ServiceControllerStatus]::Running
Show @([pscustomobject]@{Name='egw-a'; Status=$run}, [pscustomobject]@{Name='egw-b'; Status='Stopped'}, $null, [pscustomobject]@{Name='egw-c'; Status='Running'})
Show @([pscustomobject]@{Name='egw-a'; Status=$run})
Show @([pscustomobject]@{Name='egw-a'; Status='Stopped'}, [pscustomobject]@{Name='egw-b'; Status='StopPending'})
Show @()`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := outputLines(out, "NAMES=")
	want := []string{"egw-a,egw-c|COUNT=2", "egw-a|COUNT=1", "|COUNT=0", "|COUNT=0"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q, want %q\n%s", got, want, out)
	}
}

func TestServiceRestore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, action, start, want string
	}{
		{"action throws", "throw 'boom'", "$calls.Add($n)", "ERR=boom|CALLS=a,b"},
		{"first start throws", "throw 'boom'", "$calls.Add($n); if ($n -eq 'a') { throw 'start a failed' }", "ERR=boom|CALLS=a,b"},
		{"action succeeds", "'did work'", "$calls.Add($n)", "ERR=|CALLS="},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			out, err := runPS(t, `$calls = [System.Collections.Generic.List[string]]::new()
$e = ''
try { Invoke-EgwWithServiceRestore -Services 'a','b' -Action { `+c.action+` } -StartService { param($n) `+c.start+` } | Out-Null } catch { $e = $_.Exception.Message }
'RESULT=ERR=' + $e + '|CALLS=' + ($calls -join ',')`)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if got := outputLines(out, "RESULT="); len(got) != 1 || got[0] != c.want {
				t.Errorf("got RESULT=%q, want exactly %q\n%s", got, c.want, out)
			}
		})
	}
}

// TestUpdateScriptWiring parses update.ps1 (it cannot run here) and checks it uses the helpers:
// the running set is taken before the restore wrapper, which gets it as -Services and holds the stop
// loop, the egw.exe copy through Copy-EgwFileWithRetry and the config copies; the start loop comes
// after it; no plain Copy-Item of egw.exe and no fixed sleep before the copy remain.
func TestUpdateScriptWiring(t *testing.T) {
	t.Parallel()
	script, err := filepath.Abs("update.ps1")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runPS(t, `$ast = [System.Management.Automation.Language.Parser]::ParseFile('`+script+`', [ref]$null, [ref]$null)
$inv = @($ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.CommandAst] -and $n.GetCommandName() -eq 'Invoke-EgwWithServiceRestore' }, $true))
'INVOKES=' + $inv.Count
if ($inv.Count -ne 1) { return }
$inv = $inv[0]
'INVOKE=' + ($inv.Extent.Text -split "`+"`"+`n")[0].Trim()
$action = $inv.CommandElements | Where-Object { $_ -is [System.Management.Automation.Language.ScriptBlockExpressionAst] } | Select-Object -First 1
foreach ($a in $ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.AssignmentStatementAst] }, $true)) {
    if ($a.Left.Extent.Text -eq '$running') { 'ASSIGN=' + $(if ($a.Extent.EndOffset -lt $inv.Extent.StartOffset) { 'before' } else { 'after' }) + ' ' + $a.Right.Extent.Text.Split(' ')[0] }
}
foreach ($c in $ast.FindAll({ param($n) $n -is [System.Management.Automation.Language.CommandAst] }, $true)) {
    $s = $c.Extent.StartOffset
    $where = if ($s -lt $inv.Extent.StartOffset) { 'before' } elseif ($action -and $s -ge $action.Extent.StartOffset -and $c.Extent.EndOffset -le $action.Extent.EndOffset) { 'action' } elseif ($s -ge $inv.Extent.EndOffset) { 'after' } else { 'invoke' }
    'CMD=' + $where + ' ' + $c.GetCommandName() + ' ' + $(if ($c.Extent.Text -match 'egw\.exe') { 'egw.exe' } else { '-' })
}`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := outputLines(out, "INVOKES="); len(got) != 1 || got[0] != "1" {
		t.Fatalf("want exactly one Invoke-EgwWithServiceRestore call, got %q\n%s", got, out)
	}
	if got := outputLines(out, "INVOKE="); len(got) != 1 || got[0] != "Invoke-EgwWithServiceRestore -Services $running -Action {" {
		t.Errorf("restore call is %q, want it to pass -Services $running and an -Action block", got)
	}
	if got := outputLines(out, "ASSIGN="); len(got) != 1 || got[0] != "before Select-EgwRunningServices" {
		t.Errorf("$running assignments %q, want one Select-EgwRunningServices before the restore call", got)
	}
	cmds := outputLines(out, "CMD=")
	index := func(entry string) int {
		for i, c := range cmds {
			if c == entry {
				return i
			}
		}
		return -1
	}
	stop, copyExe, copyCfg, start := index("action Stop-Service -"), index("action Copy-EgwFileWithRetry egw.exe"), index("action Copy-Item -"), index("after Start-Service -")
	if stop < 0 || copyExe < 0 || copyCfg < 0 || start < 0 || !(stop < copyExe && copyExe < copyCfg && copyCfg < start) {
		t.Errorf("want Stop-Service, Copy-EgwFileWithRetry egw.exe, config Copy-Item inside the action and Start-Service after it, in that order (indexes %d %d %d %d)\n%s", stop, copyExe, copyCfg, start, strings.Join(cmds, "\n"))
	}
	for _, c := range cmds {
		f := strings.Fields(c)
		if f[1] == "Copy-Item" && f[2] == "egw.exe" {
			t.Errorf("plain Copy-Item of egw.exe left: %s", c)
		}
		if f[1] == "Start-Sleep" && f[0] != "after" {
			t.Errorf("fixed sleep before the copy left: %s", c)
		}
		if f[1] == "Stop-Service" && f[0] != "action" {
			t.Errorf("Stop-Service outside the restore wrapper: %s", c)
		}
	}
}
