// Temporary diagnostic: verify a saved chrome profile still carries the
// account session (localStorage user.hashed_token) by evaluating JS over CDP.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
)

type target struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func main() {
	port := "9301"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	base := "http://127.0.0.1:" + port
	resp, err := http.Get(base + "/json/list")
	if err != nil {
		fmt.Println("list error:", err)
		return
	}
	var targets []target
	_ = json.NewDecoder(resp.Body).Decode(&targets)
	resp.Body.Close()

	var ws string
	var pageURL string
	for _, t := range targets {
		if t.Type == "page" && t.WebSocketDebuggerURL != "" {
			ws, pageURL = t.WebSocketDebuggerURL, t.URL
			break
		}
	}
	if ws == "" {
		fmt.Println("no page target on", port)
		return
	}
	fmt.Println("page:", pageURL)

	conn, _, err := websocket.DefaultDialer.Dial(ws, nil)
	if err != nil {
		fmt.Println("dial error:", err)
		return
	}
	defer conn.Close()

	js := `(() => {
		let u = {};
		try { u = JSON.parse(localStorage.getItem('user') || '{}'); } catch (e) {}
		return JSON.stringify({
			logged_in: !!u.hashed_token,
			uid: u.id || '',
			email: u.email || '',
			onboarded: u.onboarded === true,
			autoRoll_text: (u.settings && u.settings.autoRoll) ? u.settings.autoRoll.text : null,
			privacy_hidden: (u.settings && u.settings.privacy) ? u.settings.privacy.hidden : null,
			ls_keys: Object.keys(localStorage).length,
			origin: location.origin
		});
	})()`
	req := map[string]any{
		"id":     1,
		"method": "Runtime.evaluate",
		"params": map[string]any{"expression": js, "returnByValue": true},
	}
	if err := conn.WriteJSON(req); err != nil {
		fmt.Println("write error:", err)
		return
	}
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			fmt.Println("read error:", err)
			return
		}
		var out struct {
			ID     int `json:"id"`
			Result struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			} `json:"result"`
		}
		if json.Unmarshal(msg, &out) == nil && out.ID == 1 {
			fmt.Println("session:", out.Result.Result.Value)
			return
		}
	}
}
