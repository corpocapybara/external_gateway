package deploy

// Tests for deploylib.ps1, the helpers deploy\update.ps1 uses so a failed deploy (egw.exe still
// locked, a stop timeout, a config copy) never leaves the gateways stopped (EG0072). Each test runs
// the helpers in one powershell.exe; no service is touched.

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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Release the lock only once the helper has hit it and said it retries.
	var out bytes.Buffer
	sc := bufio.NewScanner(stdout)
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
	if !strings.Contains(out, "copy to "+dst+" still failing after 2 s") {
		t.Errorf("error does not name the destination:\n%s", out)
	}
	syscall.CloseHandle(h)
	h = 0
	assertContent(t, dst, "old")
}

func TestCopyWithRetryMissingSource(t *testing.T) {
	t.Parallel()
	_, dst := copyPair(t)
	missing := filepath.Join(filepath.Dir(dst), "absent.exe")

	start := time.Now()
	out, err := runPS(t, "Copy-EgwFileWithRetry -Source '"+missing+"' -Destination '"+dst+"' -TimeoutSeconds 30")
	if err == nil {
		t.Fatalf("helper succeeded with a missing source:\n%s", out)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("missing source took %v, want under 5 s (no retry)", d)
	}
	if strings.Contains(out, "retrying") {
		t.Errorf("missing source was retried:\n%s", out)
	}
	assertContent(t, dst, "old")
}

func TestSelectRunningServices(t *testing.T) {
	t.Parallel()
	out, err := runPS(t, `$in = @(
  [pscustomobject]@{Name='egw-a'; Status='Running'},
  [pscustomobject]@{Name='egw-b'; Status='Stopped'},
  $null,
  [pscustomobject]@{Name='egw-c'; Status='Running'})
'NAMES=' + ((Select-EgwRunningServices $in) -join ',')`)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "NAMES=egw-a,egw-c\r\n") && !strings.Contains(out, "NAMES=egw-a,egw-c\n") {
		t.Errorf("want NAMES=egw-a,egw-c, got:\n%s", out)
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
			if !strings.Contains(out, "RESULT="+c.want) {
				t.Errorf("want RESULT=%s, got:\n%s", c.want, out)
			}
		})
	}
}
