//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func isElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// relaunchElevated starts this executable again through the UAC prompt.
func relaunchElevated(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = syscall.EscapeArg(a)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, verb, file, params, dir, windows.SW_HIDE)
}

func serviceState(name string) (installed bool, state string) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, "unknown (" + err.Error() + ")"
	}
	defer windows.CloseServiceHandle(scm)
	svcName, _ := windows.UTF16PtrFromString(name)
	h, err := windows.OpenService(scm, svcName, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false, "not installed"
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(h, &st); err != nil {
		return true, "unknown"
	}
	switch svc.State(st.CurrentState) {
	case svc.Running:
		return true, "running"
	case svc.Stopped:
		return true, "stopped"
	case svc.StartPending:
		return true, "starting"
	case svc.StopPending:
		return true, "stopping"
	}
	return true, fmt.Sprint(st.CurrentState)
}

func controlService(name, action string) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(name)
	if err != nil {
		return fmt.Errorf("service %s: %w", name, err)
	}
	defer s.Close()
	waitFor := func(want svc.State) error {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			st, err := s.Query()
			if err == nil && st.State == want {
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return fmt.Errorf("service %s did not reach state %d", name, want)
	}
	if action == "stop" || action == "restart" {
		if st, err := s.Query(); err == nil && st.State != svc.Stopped {
			if _, err := s.Control(svc.Stop); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
			if err := waitFor(svc.Stopped); err != nil {
				return err
			}
		}
	}
	if action == "start" || action == "restart" {
		if err := s.Start(); err != nil {
			return fmt.Errorf("start: %w", err)
		}
		return waitFor(svc.Running)
	}
	return nil
}

// openWindow shows the UI as an app window via Edge when available, otherwise in the default browser.
func openWindow(u string) {
	for _, p := range []string{
		os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`,
		os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			if exec.Command(p, "--app="+u, "--new-window").Start() == nil {
				return
			}
		}
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
}

// openUnelevated asks Explorer (which runs unelevated) to open the URL, so the browser
// does not inherit this process's admin token.
func openUnelevated(u string) {
	exec.Command("explorer.exe", u).Start()
}

var instanceMutex windows.Handle

// acquireSingleInstance holds a per-user, per-data-dir mutex for the lifetime of the process.
func acquireSingleInstance(dataDir string) bool {
	name, _ := windows.UTF16PtrFromString(`Local\ExternalGateway-` + sha256Hex(strings.ToLower(dataDir))[:16])
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		windows.CloseHandle(h)
		return false
	}
	instanceMutex = h
	return true
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const runValue = "ExternalGateway"

func autostartCommand() string {
	exe, _ := os.Executable()
	return `"` + exe + `" -tray`
}

func autostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValue)
	return err == nil && strings.EqualFold(v, autostartCommand())
}

func setAutostart(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(runValue, autostartCommand())
	}
	if err := k.DeleteValue(runValue); err != nil && err != registry.ErrNotExist {
		return err
	}
	return nil
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// showToast raises a Windows notification through Windows PowerShell's registered app id.
func showToast(title, body string) {
	xml := "<toast><visual><binding template='ToastGeneric'><text>" + xmlEscape(title) + "</text><text>" + xmlEscape(body) + "</text></binding></visual></toast>"
	script := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] | Out-Null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] | Out-Null
$x = New-Object Windows.Data.Xml.Dom.XmlDocument
$x.LoadXml('` + strings.ReplaceAll(xml, "'", "''") + `')
$t = New-Object Windows.UI.Notifications.ToastNotification $x
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe').Show($t)`
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[i*2] = byte(c)
		b[i*2+1] = byte(c >> 8)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Start()
}
