//go:build windows

// ExternalGateway is the desktop companion for the gateway: a tray app with a local UI that shows
// every workspace's secrets, services, capabilities and agent tokens, checks them in the background,
// notifies about problems and edits the configs.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type settings struct {
	CheckIntervalMin int  `json:"check_interval_min"`
	Notify           bool `json:"notify"`
	ExpiryWarnDays   int  `json:"expiry_warn_days"`
}

type app struct {
	buildDir   string
	installDir string
	dataDir    string
	session    string
	port       int
	elevated   bool

	mu          sync.Mutex
	adminTokens map[string]string
	settings    settings
	results     map[string]*wsResult
	lastCheck   time.Time
	notified    map[string]bool
	checking    bool
	rechecking  map[string]int
	onChange    func()
}

type instanceFile struct {
	Port    int    `json:"port"`
	Session string `json:"session"`
}

func main() {
	exeDir := "."
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	buildDir := flag.String("build-dir", exeDir, "external_gateway checkout (config.<ws>.yaml, operations.<ws>.yaml, agent-keys.txt, deploy\\)")
	installDir := flag.String("install-dir", `C:\Program Files\external_gateway`, "gateway install dir (<ws>\\config.yaml)")
	tray := flag.Bool("tray", false, "start in the tray without opening the window")
	helper := flag.String("helper", "", "internal: elevated helper action")
	result := flag.String("result", "", "internal: helper result file")
	ws := flag.String("ws", "", "internal: helper workspace")
	action := flag.String("action", "", "internal: helper service action")
	restart := flag.Bool("restart", false, "internal: helper restarts the service after publishing")
	port := flag.Int("port", 0, "UI port (default: random)")
	dataDir := flag.String("data-dir", filepath.Join(os.Getenv("APPDATA"), "ExternalGateway"), "settings, state and backups")
	flag.Parse()

	a := &app{
		buildDir: *buildDir, installDir: *installDir, dataDir: *dataDir, elevated: isElevated(),
		adminTokens: map[string]string{}, results: map[string]*wsResult{}, notified: map[string]bool{},
		rechecking: map[string]int{},
	}

	if *helper != "" {
		runHelper(a, *helper, *result, *ws, *action, *restart)
		return
	}

	os.MkdirAll(a.dataDir, 0700)
	if !acquireSingleInstance(a.dataDir) {
		if !*tray {
			a.signalRunningInstance()
		}
		return
	}

	a.loadSettings()
	a.loadState()
	a.session = randomToken(24)
	var l net.Listener
	var err error
	if prev, ok := a.previousInstance(); ok && *port == 0 {
		if l, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", prev.Port)); err == nil {
			a.session = prev.Session
		}
	}
	if l == nil {
		if l, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port)); err != nil {
			log.Fatal(err)
		}
	}
	a.port = l.Addr().(*net.TCPAddr).Port
	inst, _ := json.Marshal(instanceFile{Port: a.port, Session: a.session})
	os.WriteFile(filepath.Join(a.dataDir, "instance.json"), inst, 0600)

	go func() { log.Fatal(a.serve(l)) }()
	go a.checkLoop()
	if !*tray {
		go a.openUI()
	}
	runTray(a)
}

func (a *app) uiURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/?s=%s", a.port, a.session)
}

func (a *app) openUI() {
	if a.elevated {
		openUnelevated(a.uiURL())
	} else {
		openWindow(a.uiURL())
	}
}

// previousInstance returns the port and session of the last run, so windows opened before a restart keep working.
func (a *app) previousInstance() (instanceFile, bool) {
	var inst instanceFile
	data, err := os.ReadFile(filepath.Join(a.dataDir, "instance.json"))
	if err != nil || json.Unmarshal(data, &inst) != nil || inst.Port == 0 || len(inst.Session) < 16 {
		return inst, false
	}
	return inst, true
}

func (a *app) signalRunningInstance() {
	inst, ok := a.previousInstance()
	if !ok {
		return
	}
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/open-window", inst.Port), bytes.NewReader([]byte("{}")))
	req.Header.Set("X-Session", inst.Session)
	req.Header.Set("Content-Type", "application/json")
	c := http.Client{Timeout: 5 * time.Second}
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
	}
}

func (a *app) loadSettings() {
	a.settings = settings{CheckIntervalMin: 30, Notify: true, ExpiryWarnDays: 14}
	if data, err := os.ReadFile(filepath.Join(a.dataDir, "settings.json")); err == nil {
		json.Unmarshal(data, &a.settings)
	}
	if a.settings.CheckIntervalMin < 5 {
		a.settings.CheckIntervalMin = 5
	}
	if a.settings.ExpiryWarnDays <= 0 {
		a.settings.ExpiryWarnDays = 14
	}
}

func (a *app) saveSettings() error {
	data, _ := json.MarshalIndent(a.settings, "", "  ")
	return os.WriteFile(filepath.Join(a.dataDir, "settings.json"), data, 0600)
}

type stateFile struct {
	Notified map[string]bool `json:"notified"`
}

func (a *app) loadState() {
	if data, err := os.ReadFile(filepath.Join(a.dataDir, "state.json")); err == nil {
		var st stateFile
		if json.Unmarshal(data, &st) == nil && st.Notified != nil {
			a.notified = st.Notified
		}
	}
}

func (a *app) saveState() {
	data, _ := json.Marshal(stateFile{Notified: a.notified})
	os.WriteFile(filepath.Join(a.dataDir, "state.json"), data, 0600)
}
