// Package redact masks secret-shaped values in log lines. Logs are piped to
// the UI, the OnLog hook and screenshots — they must never carry credentials
// (API keys, passwords, tokens, proxy userinfo). Ports the reference project's
// credential-redaction discipline.
package redact

import (
	"regexp"
)

var (
	// prefixed keys: sk-..., rk-..., ghp_..., xoxb-..., AKIA...
	rePrefixed = regexp.MustCompile(`(?i)\b(sk|rk|pk|ghp|gho|ghu|xoxb|xoxp|akia)[_-][A-Za-z0-9_\-]{6,}`)
	// bearer / authorization header values
	reBearer = regexp.MustCompile(`(?i)(bearer|authorization)\s+[A-Za-z0-9._\-+/=]{8,}`)
	// key[:=]"value" pairs with a secret-looking key
	reCredKV = regexp.MustCompile(`(?i)\b(password|passwd|pwd|pass|secret|api_?key|access_?token|hashed_?token|token)\b(["']?)\s*([:=])\s*("?)([^\s,;}"']{3,})`)
	// user:pass@ userinfo inside URLs (proxy lines)
	reUserInfo = regexp.MustCompile(`(?i)([a-z0-9._%+\-]{2,}):([^\s@/]{2,})@`)
)

// Redact returns line with every secret-shaped value masked to "***".
func Redact(line string) string {
	out := rePrefixed.ReplaceAllStringFunc(line, func(m string) string {
		return m[:4] + "***"
	})
	out = reBearer.ReplaceAllString(out, "${1} ***")
	// ${1} key-quote ${3} sep ${4} quote → keep shape, mask value
	out = reCredKV.ReplaceAllString(out, "${1}${2}${3}${4}***")
	out = reUserInfo.ReplaceAllString(out, "${1}:***@")
	return out
}