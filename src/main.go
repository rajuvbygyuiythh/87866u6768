// thundr-bot - single-binary Thundr bot: DESKTOP app (wails, native window)
// like the ChitChat reference project. No browser tab needed.
//
// Flags:
//
//	thundr-bot.exe                 desktop app (default)
//	thundr-bot.exe -server         headless HTTP dashboard only (127.0.0.1:8787)
//
// Layout next to the exe:
//
//	data/              settings.txt, registered_*.txt, telegram.txt, msg/, sessions/, webview/
//	browser-profiles/  persistent chrome profiles (reg-live + one per account)
//	tools/             chrome-watcher.ps1 + install-chrome-task.ps1
//	src/               this Go source tree
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"thundr/internal/dash"
)

//go:embed all:frontend/dist
var embeddedAssets embed.FS

func main() {
	// Run from the exe's own folder: a dashboard started by a shortcut, a
	// script or a service can inherit a dead/UNC working directory, which makes
	// every child process (chrome) fail with "cannot find the path".
	if dir := exeDir(); dir != "" {
		_ = os.Chdir(dir)
	}
	port := flag.Int("port", 8787, "dashboard/API port")
	noBrowser := flag.Bool("no-browser", false, "server mode: do not auto-open the browser")
	serverMode := flag.Bool("server", false, "run headless HTTP dashboard only (no desktop window)")
	dataDir := flag.String("data", filepath.Join(exeDir(), "data"), "data directory")
	flag.Parse()

	if *serverMode {
		app := dash.Open(*dataDir)
		if !*noBrowser {
			go func() {
				time.Sleep(700 * time.Millisecond)
				openBrowser(fmt.Sprintf("http://127.0.0.1:%d", *port))
			}()
		}
		if err := app.Serve(fmt.Sprintf("127.0.0.1:%d", *port)); err != nil {
			fmt.Fprintln(os.Stderr, "dashboard:", err)
			os.Exit(1)
		}
		return
	}

	if err := runDesktop(*dataDir, *port); err != nil {
		fmt.Fprintln(os.Stderr, "desktop:", err)
		os.Exit(1)
	}
}

func runDesktop(dataDir string, port int) error {
	dapp := dash.Open(dataDir)
	desktop := NewDesktopApp(dapp, port)

	dist, err := fs.Sub(embeddedAssets, "frontend/dist")
	if err != nil {
		return err
	}

	// Keep the WebView2 profile inside this copy's data dir (like the
	// reference project) so multiple copies never clash.
	webviewDir := filepath.Join(dataDir, "webview")
	_ = os.MkdirAll(webviewDir, 0o755)

	return wails.Run(&options.App{
		Title:              "Thundr Bot",
		Width:              1280,
		Height:             820,
		MinWidth:           1080,
		MinHeight:          700,
		BackgroundColour:   &options.RGBA{R: 22, G: 23, B: 22, A: 255},
		LogLevelProduction: logger.INFO,
		AssetServer: &assetserver.Options{
			Assets: dist,
		},
		OnStartup:  desktop.startup,
		OnShutdown: desktop.shutdown,
		Bind: []interface{}{
			desktop,
		},
		Windows: &windows.Options{
			Theme:               windows.SystemDefault,
			WebviewUserDataPath: webviewDir,
			ResizeDebounceMS:    10,
		},
	})
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func openBrowser(url string) {
	if runtime.GOOS == "windows" {
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		return
	}
	_ = exec.Command("xdg-open", url).Start()
}
