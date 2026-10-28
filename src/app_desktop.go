package main

// Desktop shell: wails bindings that the frontend calls via window.go.main.DesktopApp.*.
// All real work lives in internal/dash (same engine the HTTP dashboard uses).

import (
	"context"
	"fmt"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"thundr/internal/dash"
	"thundr/internal/reply"
)

// DesktopApp is the binding object exposed to the frontend.
type DesktopApp struct {
	ctx  context.Context
	dapp *dash.App
	port int
}

func NewDesktopApp(d *dash.App, port int) *DesktopApp {
	return &DesktopApp{dapp: d, port: port}
}

func (d *DesktopApp) startup(ctx context.Context) {
	d.ctx = ctx
	// stream log lines to the frontend + start the HTTP API as a bonus/fallback
	d.dapp.OnLog = func(line string) {
		wailsRuntime.EventsEmit(d.ctx, "log", line)
		wailsRuntime.EventsEmit(d.ctx, "ui:log", line) // kit contract: trigger an immediate poll
	}
	d.dapp.StartEventDrain()
	go func() { _ = d.dapp.Serve(fmt.Sprintf("127.0.0.1:%d", d.port)) }()
}

func (d *DesktopApp) shutdown(_ context.Context) {
	_, _ = d.dapp.StopBots()
}

// ---- bindings ---------------------------------------------------------------

func (d *DesktopApp) GetState() dash.Snapshot { return d.dapp.GetSnapshot() }

func (d *DesktopApp) RegisterAccounts(count int) map[string]any {
	ok, msg := d.dapp.RegisterAccounts(count)
	return map[string]any{"ok": ok, "message": msg}
}

func (d *DesktopApp) StartBots(uids []string, mode string) map[string]any {
	ok, msg := d.dapp.StartBots(uids, mode)
	return map[string]any{"ok": ok, "message": msg}
}

func (d *DesktopApp) StopBots() map[string]any {
	ok, msg := d.dapp.StopBots()
	return map[string]any{"ok": ok, "message": msg}
}

func (d *DesktopApp) OpenBrowser(target, uid string) map[string]any {
	ok, attached := d.dapp.OpenChrome(target, uid)
	return map[string]any{"ok": ok, "attached": attached}
}

func (d *DesktopApp) GetFile(name string) map[string]any {
	return map[string]any{"content": d.dapp.Store().ReadFile(name)}
}

func (d *DesktopApp) SaveFile(name, content string) map[string]any {
	return map[string]any{"ok": d.dapp.Store().WriteFile(name, content)}
}

func (d *DesktopApp) GetMessageSettings() map[string]any { return d.dapp.GetMessageSettings() }

func (d *DesktopApp) SaveMessageSettings(s reply.Settings) map[string]any {
	return map[string]any{"ok": d.dapp.SaveMessageSettings(s)}
}

// LoadMessageFolder loads sequences from the payload's folder path and
// remembers it as the default (payload shape: {"path": "D:\\sms"}).
func (d *DesktopApp) LoadMessageFolder(payload map[string]any) map[string]any {
	path, _ := payload["path"].(string)
	n, err := d.dapp.LoadSequences(path)
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	return map[string]any{"ok": true, "count": n}
}

func (d *DesktopApp) CleanLoadedMessages() map[string]any {
	d.dapp.CleanSequences()
	return map[string]any{"ok": true}
}

func (d *DesktopApp) DeleteAccount(uid string) map[string]any {
	d.dapp.Store().DeleteAccount(uid)
	return map[string]any{"ok": true}
}

func (d *DesktopApp) TestProxy(proxy string) map[string]any {
	return d.dapp.TestProxyEntry(proxy)
}

// GetBrowserStatus reports chrome/watcher/debug-port state for the desktop UI.
func (d *DesktopApp) GetBrowserStatus() map[string]any { return d.dapp.BrowserStatus() }

// CloseBrowsers closes every chrome the app owns.
func (d *DesktopApp) CloseBrowsers() map[string]any {
	d.dapp.CloseBrowsers()
	return map[string]any{"ok": true}
}
