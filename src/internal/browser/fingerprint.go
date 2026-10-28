package browser

// Fingerprint capture from the live chrome page (same JS fields as the
// reference project's fingerprintFunction). The struct itself lives in the
// store package to avoid an import cycle.

import (
	"context"

	"github.com/chromedp/chromedp"

	"thundr/internal/store"
)

// CaptureJS reads the live fingerprint from the page.
const CaptureJS = `(() => ({
  userAgent: navigator.userAgent,
  language: navigator.language,
  colorDepth: String(screen.colorDepth),
  screenResolution: String(screen.width) + "x" + String(screen.height),
  timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  addBehavior: String("addBehavior" in document.documentElement),
  openDatabase: String("openDatabase" in window),
  plugins: Array.from(navigator.plugins).map((plugin) => plugin.name).join(","),
  canvas: (() => {
    const canvas = document.createElement("canvas");
    const context = canvas.getContext("2d");
    if (context) { context.fillText("BF", 2, 2); }
    return canvas.toDataURL().slice(-50);
  })(),
  webgl: (() => {
    const context = document.createElement("canvas").getContext("webgl");
    if (!context) { return "~"; }
    return String(context.getParameter(context.VENDOR)) + "~" + String(context.getParameter(context.RENDERER));
  })(),
  touchSupport: String("ontouchstart" in window),
  hardwareConcurrency: String(navigator.hardwareConcurrency || "unknown"),
}))()`

// CaptureFingerprint evaluates CaptureJS in the current page.
func CaptureFingerprint(ctx context.Context) (store.Fingerprint, error) {
	var fp store.Fingerprint
	err := chromedp.Run(ctx, chromedp.Evaluate(CaptureJS, &fp))
	return fp, err
}
