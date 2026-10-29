package reply

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"thundr/internal/store"
)

// EVAChat Flow mode: a stateful, TXT-driven reply funnel. The editable data
// lives in <data>/eva_chat/ (seeded from the embedded starter on first run):
//
//	eva_chat/input/<cat>.txt    user triggers, one per line ('#' = comment)
//	eva_chat/output/<cat>.txt   bot replies, one per line ('|' also splits)
//
// A category is connected by file name: input/x.txt detects, output/x.txt
// replies. Adding a new pair adds a category without any code change.
//
// Funnel summary (same rules as the ChitChat reference):
//   - opener from output/greeting.txt
//   - category match -> reply from that output file (once per chat, except
//     the repeatable whitelist below)
//   - no match -> middle_chat pools in rotation (warm -> flirty -> busy)
//   - every 3rd unmatched reply -> ask_snap.txt (the funnel toward the share)
//   - share_snap/snapchat match, or "yes" after ask_snap -> share_snap.txt
//     with %username% -> chat ends
//   - "no" after ask_snap -> retry_nudge once; second "no" -> closer -> end
//   - reply cap 8 -> closer -> end

//go:embed all:eva_assets
var evaAssets embed.FS

// SeedEVA writes the starter TXT dataset when the folder is missing.
func SeedEVA(st *store.Store) error { return seedDir(evaAssets, "eva_assets", st.DirPath("eva_chat")) }

// SeedAPI writes the starter API Chat dataset when the folder is missing.
func SeedAPI(st *store.Store) error { return seedDir(apiAssets, "api_assets", st.DirPath("api_chat")) }

func seedDir(assets embed.FS, from, to string) error {
	return fs.WalkDir(assets, from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if _, err := os.Stat(target); err == nil {
			return nil // never overwrite user edits
		}
		data, err := assets.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// ---------------------------------------------------------------------------
// pools
// ---------------------------------------------------------------------------

type evaPools struct {
	inputs  map[string][]string // category -> trigger lines
	outputs map[string][]string // category -> reply lines
}

func readPoolFile(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		// output files: '|' also splits lines
		for _, part := range strings.Split(l, "|") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func loadEVADir(root string) map[string][]string {
	out := map[string][]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".txt") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		cat := strings.ReplaceAll(strings.TrimSuffix(rel, filepath.Ext(rel)), string(filepath.Separator), "/")
		if lines := readPoolFile(p); len(lines) > 0 {
			out[cat] = lines
		}
		return nil
	})
	return out
}

func loadEVAPools(st *store.Store) *evaPools {
	root := st.DirPath("eva_chat")
	return &evaPools{
		inputs:  loadEVADir(filepath.Join(root, "input")),
		outputs: loadEVADir(filepath.Join(root, "output")),
	}
}

// evaRepeatable categories may fire more than once per chat.
var evaRepeatable = map[string]bool{
	"flirty_questions": true, "horny": true, "ask_snap": true,
	"horny_answer": true, "closer": true,
	"middle_chat/flirty_reply": true, "middle_chat/warm_reply": true,
	"middle_chat/busy_later": true,
}

var evaYes = []string{"yes", "yeah", "yep", "yup", "yea", "ya", "sure", "ok", "okay", "y", "please", "of course", "why not"}
var evaNo = []string{"no", "nope", "nah", "not now", "no thanks", "n"}

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

func normWords(s string) []string { return wordRe.FindAllString(strings.ToLower(s), -1) }

func containsPhrase(haystack []string, needle []string) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		ok := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func isOneWord(s string, list []string) bool {
	w := normWords(s)
	if len(w) == 0 || len(w) > 3 {
		return false
	}
	joined := strings.Join(w, " ")
	for _, v := range list {
		if joined == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// session
// ---------------------------------------------------------------------------

const evaReplyCap = 8

type evaSession struct {
	baseSess
	pools     *evaPools
	usedCat   map[string]bool
	midIdx    int // middle_chat rotation
	midCount  int
	askedSnap bool
	nudgeUsed bool
	ended     bool
}

func newEVASession(b baseSess, pools *evaPools) Session {
	return &evaSession{baseSess: b, pools: pools, usedCat: map[string]bool{}}
}

func (s *evaSession) Open() string {
	return s.emit(s.mgr.render(&s.baseSess, s.pickLine(s.pools.outputs["greeting"])))
}

// categoryFor returns the longest-trigger matching category ("" = no match).
func (s *evaSession) categoryFor(incoming string) string {
	words := normWords(incoming)
	if len(words) == 0 {
		return ""
	}
	best, bestLen := "", 0
	for cat, triggers := range s.pools.inputs {
		if strings.HasPrefix(cat, "middle_chat/") {
			continue // middle_chat is the fallback rotation, not a matcher
		}
		if s.usedCat[cat] && !evaRepeatable[cat] {
			continue
		}
		for _, trg := range triggers {
			tw := normWords(trg)
			if len(tw) > bestLen && containsPhrase(words, tw) {
				best, bestLen = cat, len(tw)
			}
		}
	}
	return best
}

func (s *evaSession) replyFrom(cat string) string {
	s.usedCat[cat] = true
	return s.mgr.render(&s.baseSess, s.pickLine(s.pools.outputs[cat]))
}

// shareLine renders the share message with this chat's assigned handle.
func (s *evaSession) shareLine() string {
	pool := s.pools.outputs["share_snap"]
	if len(pool) == 0 {
		pool = s.pools.outputs["snapchat"]
	}
	return s.mgr.render(&s.baseSess, s.pickLine(pool))
}

func (s *evaSession) closerLine() string {
	return s.mgr.render(&s.baseSess, s.pickLine(s.pools.outputs["closer"]))
}

func (s *evaSession) Next(incoming string) (string, bool) {
	if s.ended {
		return "", true
	}
	replies := s.sent
	if replies >= evaReplyCap {
		s.ended = true
		return s.emit(s.closerLine()), true
	}

	// pending ask_snap: yes -> share, no -> nudge once then closer
	if s.askedSnap {
		if isOneWord(incoming, evaYes) {
			s.ended = true
			return s.emit(s.share()), true
		}
		if isOneWord(incoming, evaNo) {
			if !s.nudgeUsed {
				s.nudgeUsed = true
				s.askedSnap = false
				return s.emit(s.replyFrom("retry_nudge")), false
			}
			s.ended = true
			return s.emit(s.closerLine()), true
		}
		// not a clear answer: keep chatting normally
		s.askedSnap = false
	}

	cat := s.categoryFor(incoming)
	switch cat {
	case "share_snap", "snapchat":
		s.ended = true
		return s.emit(s.share()), true
	case "":
		// fall through to middle rotation
	default:
		return s.emit(s.replyFrom(cat)), false
	}

	// no category matched: middle rotation, funnel toward ask_snap
	s.midCount++
	if s.midCount%3 == 0 && s.mgr.HasShareTarget() && s.handle != "" {
		s.askedSnap = true
		return s.emit(s.replyFrom("ask_snap")), false
	}
	midCats := []string{"middle_chat/warm_reply", "middle_chat/flirty_reply", "middle_chat/busy_later"}
	cat = midCats[s.midIdx%len(midCats)]
	s.midIdx++
	if pool := s.pools.outputs[cat]; len(pool) > 0 {
		s.usedCat[cat] = true
		return s.emit(s.mgr.render(&s.baseSess, s.pickLine(pool))), false
	}
	// dataset incomplete: last-resort pools
	for _, fb := range []string{"flirty_questions", "how_are_you", "greeting"} {
		if line := s.replyFrom(fb); line != "" {
			return s.emit(line), false
		}
	}
	return "", true
}

// share renders the share line (or a polite closer when no handle exists).
func (s *evaSession) share() string {
	if s.handle == "" {
		return s.closerLine()
	}
	return s.shareLine()
}

func (s *evaSession) Progress() (int, int) { return s.sent, -1 }
