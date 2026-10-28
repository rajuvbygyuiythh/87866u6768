package main

// Design-kit contract surface, bound to JS as window.go.main.DesktopApp.*.
// These are thin wrappers: all real work lives in internal/dash (contract.go),
// which also serves the identical HTTP shapes. Method names and shapes follow
// the kit's 08-API-CONTRACT.md so the starter frontend works unchanged.

import (
	"fmt"
	"strings"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"thundr/internal/reply"
)

func (d *DesktopApp) emitRunning(running bool) {
	if d.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(d.ctx, "ui:running", running)
}

// GetSnapshot returns everything the UI renders with.
func (d *DesktopApp) GetSnapshot() map[string]any { return d.dapp.ContractSnapshot() }

// SaveSettings persists one section and returns the fresh snapshot.
func (d *DesktopApp) SaveSettings(section string, values map[string]any) (map[string]any, error) {
	return d.dapp.SaveSection(section, values)
}

// TestConnection checks each entry line (proxies) and returns per-line verdicts.
func (d *DesktopApp) TestConnection(conn map[string]any) map[string]any {
	entries, _ := conn["entries"].(string)
	return map[string]any{"results": d.dapp.TestConnections(entries)}
}

// TestAPI makes one model call; returns a human sentence or an error.
func (d *DesktopApp) TestAPI(credentials map[string]any) (string, error) {
	baseURL, _ := credentials["base_url"].(string)
	apiKey, _ := credentials["api_key"].(string)
	model, _ := credentials["model"].(string)
	temp, _ := credentials["temperature"].(float64)
	return d.dapp.TestAPICall(baseURL, apiKey, model, temp)
}

// StartRun begins the bot job for every saved account (profile payload is
// accepted for contract compatibility; the bot mode comes from settings).
func (d *DesktopApp) StartRun(_ map[string]any) map[string]any {
	ok, msg := d.dapp.StartBots(nil, d.dapp.Store().Get("bot_mode", "visible"))
	d.emitRunning(ok)
	return map[string]any{"ok": ok, "message": msg}
}

// StopRun cancels the job. Idempotent: always ok, always emits running=false.
func (d *DesktopApp) StopRun() map[string]any {
	_, _ = d.dapp.StopBots()
	d.emitRunning(false)
	return map[string]any{"ok": true}
}

// LaunchLogin opens the register/manual chrome for a manual login.
func (d *DesktopApp) LaunchLogin(_ map[string]any) map[string]any {
	ok, attached := d.dapp.OpenChrome("register", "")
	msg := "browser launched"
	if attached {
		msg = "attached to the running browser"
	}
	if !ok {
		msg = "launch failed — check chrome_attach_url in settings"
	}
	return map[string]any{"ok": ok, "message": msg}
}

// GetLogs is the versioned log poll (entries with id > after only).
func (d *DesktopApp) GetLogs(after int) map[string]any { return d.dapp.LogsAfter(after) }

// ClearLogs truncates the feed.
func (d *DesktopApp) ClearLogs() map[string]any {
	d.dapp.ClearLog()
	return map[string]any{"ok": true}
}

// OpenDataFolder opens the data directory and returns its absolute path.
func (d *DesktopApp) OpenDataFolder() string { return d.dapp.OpenRootFolder() }

// LoadFolder loads message sequences from the saved folder path.
func (d *DesktopApp) LoadFolder() (map[string]any, error) { return d.dapp.LoadFolderDefault() }

// BrowseMessageFolder opens the native Windows folder picker and returns the
// picked absolute path ("" = cancelled). Defaults to the saved folder path.
func (d *DesktopApp) BrowseMessageFolder() (string, error) {
	if d.ctx == nil {
		return "", fmt.Errorf("the folder picker needs the desktop window")
	}
	opts := wailsRuntime.OpenDialogOptions{
		Title:               "Select the SMS sequences folder",
		CanCreateDirectories: true,
	}
	if s, ok := d.dapp.GetMessageSettings()["settings"].(reply.Settings); ok {
		if dir := strings.TrimSpace(s.FolderPath); dir != "" {
			opts.DefaultDirectory = dir
		}
	}
	return wailsRuntime.OpenDirectoryDialog(d.ctx, opts)
}
