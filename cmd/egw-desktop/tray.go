//go:build windows

package main

import (
	"fmt"
	"os"
	"sync"

	"fyne.io/systray"
)

func runTray(a *app) {
	systray.Run(func() { trayReady(a) }, func() { os.Exit(0) })
}

func (a *app) redeployFromTray(item *systray.MenuItem) {
	item.Disable()
	item.SetTitle("Re-deploying...")
	defer item.Enable()
	defer item.SetTitle("Re-deploy")
	if _, err := a.deploy(); err != nil {
		showToast("External Gateway: re-deploy failed", err.Error())
		return
	}
	showToast("External Gateway", "Re-deployed; the gateways were restarted")
}

func trayReady(a *app) {
	systray.SetIcon(statusIcon("unknown"))
	systray.SetTooltip("External Gateway")

	open := systray.AddMenuItem("Open External Gateway", "Show the window")
	summary := systray.AddMenuItem("Checking...", "")
	summary.Disable()
	check := systray.AddMenuItem("Check now", "Re-check every workspace")
	redeploy := systray.AddMenuItem("Re-deploy", `Run deploy\update.ps1 as administrator: egw.exe and every workspace's config, then restart the gateways`)
	systray.AddSeparator()
	autostart := systray.AddMenuItemCheckbox("Start with Windows", "Run in the tray after sign-in", autostartEnabled())
	a.mu.Lock()
	notify := systray.AddMenuItemCheckbox("Notifications", "Show a notification when a new problem appears", a.settings.Notify)
	a.mu.Unlock()
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Stop External Gateway (the gateways keep running)")

	var mu sync.Mutex
	a.onChange = func() {
		mu.Lock()
		defer mu.Unlock()
		worst, count := a.worstSeverity()
		a.mu.Lock()
		checking := a.checking
		checked := !a.lastCheck.IsZero()
		a.mu.Unlock()
		state := "ok"
		switch {
		case !checked:
			state = "unknown"
		case worst != "":
			state = worst
		}
		systray.SetIcon(statusIcon(state))
		text := "All gateways OK"
		switch {
		case checking && !checked:
			text = "Checking..."
		case count == 1:
			text = "1 problem to fix"
		case count > 1:
			text = fmt.Sprintf("%d problems to fix", count)
		}
		summary.SetTitle(text)
		systray.SetTooltip("External Gateway - " + text)
	}

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				a.openUI()
			case <-summary.ClickedCh:
				a.openUI()
			case <-check.ClickedCh:
				go a.checkAll()
			case <-redeploy.ClickedCh:
				go a.redeployFromTray(redeploy)
			case <-autostart.ClickedCh:
				want := !autostart.Checked()
				if err := setAutostart(want); err == nil {
					if want {
						autostart.Check()
					} else {
						autostart.Uncheck()
					}
				}
			case <-notify.ClickedCh:
				a.mu.Lock()
				a.settings.Notify = !a.settings.Notify
				on := a.settings.Notify
				a.saveSettings()
				a.mu.Unlock()
				if on {
					notify.Check()
				} else {
					notify.Uncheck()
				}
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
