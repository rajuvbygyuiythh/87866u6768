package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
)

const captureJS = `
if (window.__thundrLog) return;
window.__thundrLog = [];
const push = (kind, data) => { try { window.__thundrLog.push({t: new Date().toISOString().slice(11,23), kind, data: (typeof data === 'string' ? data.slice(0,400) : JSON.stringify(data).slice(0,400))}); } catch(e) {} };
document.addEventListener('click', (e) => {
	const el = e.target.closest('button, [role=button], a, label, input[type=checkbox], div[class*=toggle], span[class*=toggle]') || e.target;
	push('CLICK', (el.tagName||'?') + ' aria=' + (el.getAttribute('aria-label')||'') + ' text=' + ((el.innerText||el.value||'')+'').trim().slice(0,60) + ' cls=' + (el.className||'').toString().slice(0,80));
}, true);
const of = window.fetch;
window.fetch = function(input, init) {
	const url = (typeof input === 'string' ? input : (input && input.url) || '');
	const m = (init && init.method) || 'GET';
	const body = (init && init.body) || '';
	return of.apply(this, arguments).then(r => { if (m !== 'GET' || String(url).includes('updateUser')) push('API', m + ' ' + String(url).slice(0,90) + ' -> ' + r.status + ' body=' + String(body).slice(0,220)); return r; });
};
const oo = XMLHttpRequest.prototype.open, osend = XMLHttpRequest.prototype.send;
XMLHttpRequest.prototype.open = function(m, u) { this.__m = m; this.__u = u; return oo.apply(this, arguments); };
XMLHttpRequest.prototype.send = function(b) { if (this.__m !== 'GET' || String(this.__u).includes('updateUser')) push('XHR', this.__m + ' ' + String(this.__u).slice(0,90) + ' body=' + String(b||'').slice(0,220)); return osend.apply(this, arguments); };
const osi = Storage.prototype.setItem;
Storage.prototype.setItem = function(k, v) { if (k === 'user' || k === 'settings') push('LS-SET', k + ' = ' + String(v).slice(0,300)); return osi.apply(this, arguments); };
`

func main() {
	logf, err := os.OpenFile(os.Args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil { fmt.Println(err); return }
	defer logf.Close()
	w := func(s string) { logf.WriteString(time.Now().Format("15:04:05") + " " + s + "\n") }
	w("observer starting")
	rootCtx, cancel := chromedp.NewRemoteAllocator(context.Background(), "http://127.0.0.1:9333")
	defer cancel()
	// browser-level context to list targets
	bctx, bcx := chromedp.NewContext(rootCtx)
	defer bcx()
	var chosen target.ID
	for i := 0; i < 10 && chosen == ""; i++ {
		ts, terr := chromedp.Targets(bctx)
		if terr == nil {
			for _, t := range ts {
				if t.Type == "page" && strings.Contains(t.URL, "thundr.com") { chosen = t.TargetID; break }
			}
		}
		if chosen == "" { time.Sleep(1 * time.Second) }
	}
	if chosen == "" { w("no thundr tab found"); return }
	w("attached to tab " + string(chosen[:8]))
	c, cc := chromedp.NewContext(rootCtx, chromedp.WithTargetID(chosen))
	defer cc()
	if err := chromedp.Run(c, chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(captureJS).Do(ctx)
		return err
	})); err != nil { w("hook-err: " + err.Error()) }
	var warm string
	_ = chromedp.Run(c, chromedp.Evaluate(captureJS, &warm))
	lastSnap := ""
	for i := 0; i < 43200; i++ {
		var batch string
		err := chromedp.Run(c, chromedp.Evaluate(`(() => {
			if (!window.__thundrLog || !window.__thundrLog.length) return '';
			const out = window.__thundrLog.splice(0).map(e => e.t + ' [' + e.kind + '] ' + e.data);
			return out.join('\n');
		})()`, &batch))
		if err == nil && strings.TrimSpace(batch) != "" { w(batch) }
		if i%5 == 0 { // every ~10s: state snapshot on change
			var snap string
			_ = chromedp.Run(c, chromedp.Evaluate(`(() => {
				let ls = {}, us = {};
				try { ls = JSON.parse(localStorage.getItem('settings')||'{}'); } catch(e) {}
				try { us = (JSON.parse(localStorage.getItem('user')||'{}').settings)||{}; } catch(e) {}
				return 'LOCAL autoRoll=' + JSON.stringify(ls.autoRoll||null) + ' privacy=' + JSON.stringify(ls.privacy||null) + ' | SERVER autoRoll=' + JSON.stringify(us.autoRoll||null);
			})()`, &snap))
			if snap != lastSnap && snap != "" { w("STATE " + snap); lastSnap = snap }
		}
		time.Sleep(2 * time.Second)
	}
}

