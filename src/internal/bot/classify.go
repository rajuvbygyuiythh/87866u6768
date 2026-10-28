package bot

import (
	"strings"
	"time"
)

// MatchIntel tracks one match's signals so we can classify the partner as a
// likely bot or a real user (our goal: measure and improve real-user matches).
type MatchIntel struct {
	MatchedAt         time.Time
	SearchSecs        float64 // how long we searched before this match
	SharedInterests   int
	PartnerProfileRaw string
	FirstPartnerMsgAt time.Time
	FirstPartnerMsg   string
	OurFirstReplyAt   time.Time
	PartnerLeftAt     time.Time
	PartnerMsgCount   int
}

var spamSignals = []string{
	"snap", "snapchat", "t.me", "telegram", "onlyfans", "only fans", "of.com",
	"add me", "add on", "kik", "nuu", "nudes", "hor ny", "horny", "f20", "f 20",
	"f19", "f 19", "sell", "content", "link in", "selling", "cam", "discord.gg",
}

// Classify scores bot-like signals. >=2 = bot, 1 = unknown, 0 = real.
func (m *MatchIntel) Classify() (string, []string) {
	score := 0
	var why []string

	if !m.FirstPartnerMsgAt.IsZero() {
		d := m.FirstPartnerMsgAt.Sub(m.MatchedAt)
		if d < 1500*time.Millisecond {
			score++
			why = append(why, "instant opener (<1.5s)")
		}
		msg := strings.ToLower(m.FirstPartnerMsg)
		hits := 0
		for _, sig := range spamSignals {
			if strings.Contains(msg, sig) {
				hits++
			}
		}
		if hits > 0 {
			score += 2
			why = append(why, "spam content")
		}
	}
	if !m.PartnerLeftAt.IsZero() && !m.OurFirstReplyAt.IsZero() {
		if m.PartnerLeftAt.Sub(m.OurFirstReplyAt) < 3*time.Second && m.PartnerMsgCount <= 1 {
			score++
			why = append(why, "left instantly after our reply")
		}
	}
	// real users almost always exchange >2 messages in a kept match
	if m.PartnerLeftAt.IsZero() || m.PartnerMsgCount >= 3 {
		if score < 2 {
			return "real", why
		}
	}
	switch {
	case score >= 2:
		return "bot", why
	case score == 1:
		return "unknown", why
	default:
		return "real", why
	}
}
