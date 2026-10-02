package bot

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	"cs2bot/internal/config"
	"cs2bot/internal/domain"
)

// streamLabel определяет подпись ссылки по хосту.
func streamLabel(rawURL string) string {
	u := strings.ToLower(rawURL)
	switch {
	case strings.Contains(u, "twitch.tv"):
		return "Twitch"
	case strings.Contains(u, "youtube.com"), strings.Contains(u, "youtu.be"):
		return "YouTube"
	case strings.Contains(u, "kick.com"):
		return "Kick"
	default:
		return "Трансляция"
	}
}

func twitchSearchURL(teamA, teamB string) string {
	return "https://www.twitch.tv/search?term=" + url.QueryEscape(teamA+" vs "+teamB)
}

func youtubeSearchURL(teamA, teamB string) string {
	return "https://www.youtube.com/results?search_query=" + url.QueryEscape(teamA+" vs "+teamB+" cs2 live")
}

// streamLinkText возвращает подпись ссылки: "канал (Платформа)" для
// Twitch/Kick, где канал извлекается из пути URL. Для YouTube и прочих —
// просто платформа (в watch-ссылке имени канала нет).
func streamLinkText(rawURL string) string {
	platform := streamLabel(rawURL)
	if channel := streamChannel(rawURL); channel != "" {
		return channel + " (" + platform + ")"
	}
	return platform
}

// streamChannel извлекает имя канала из URL.
// Twitch: twitch.tv/<channel> (embed: ?channel=xxx), Kick: kick.com/<channel>.
// Служебные разделы и прочие хосты дают "" — рендер откатится на платформу.
func streamChannel(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	isTwitch := strings.Contains(host, "twitch.tv")
	isKick := strings.Contains(host, "kick.com")
	if !isTwitch && !isKick {
		return ""
	}
	// Embed-плеер Twitch: player.twitch.tv/?channel=xxx
	if isTwitch {
		if ch := strings.TrimSpace(u.Query().Get("channel")); ch != "" {
			return truncateChannel(ch)
		}
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return ""
	}
	ch := segs[0]
	// Служебные разделы — не каналы.
	switch strings.ToLower(ch) {
	case "directory", "search", "videos", "clip", "clips", "collections",
		"event", "events", "drops", "subs", "settings", "wallet",
		"downloads", "jobs", "p":
		return ""
	}
	return truncateChannel(ch)
}

// truncateChannel чистит и режет имя канала от мусора в URL.
func truncateChannel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if r := []rune(s); len(r) > 32 {
		s = string(r[:32])
	}
	return s
}

func fallbackLine(m domain.Match) string {
	return fmt.Sprintf(`🔍 Найти трансляцию: <a href="%s">Twitch</a> | <a href="%s">YouTube</a>`,
		html.EscapeString(twitchSearchURL(m.TeamA, m.TeamB)),
		html.EscapeString(youtubeSearchURL(m.TeamA, m.TeamB)))
}

// streamLines возвращает строки трансляций:
//   - "⭐ Официальная трансляция: ..." — все стримы с official=true (без лимита);
//   - "📺 Смотреть: ..." — остальные, с лимитом (limit: 0 = все неофициальные);
//   - "🔍 Найти трансляцию: ..." — fallback на поиск, всегда последней строкой.
//
// Если официальных нет — звездочной строки нет. Если стримов нет вообще —
// только fallback одной строкой.
func streamLines(m domain.Match, limit int) []string {
	limit = config.NormalizeMaxStreams(limit)
	var official, rest []domain.MatchStream
	for _, s := range m.Streams {
		if strings.TrimSpace(s.URL) == "" {
			continue
		}
		if s.Official {
			official = append(official, s)
		} else {
			rest = append(rest, s)
		}
	}
	var lines []string
	if len(official) > 0 {
		links := make([]string, 0, len(official))
		for _, s := range official {
			links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`,
				html.EscapeString(s.URL), html.EscapeString(streamLinkText(s.URL))))
		}
		lines = append(lines, "⭐ Официальная трансляция: "+strings.Join(links, " | "))
	}
	shown := 0
	links := make([]string, 0, len(rest))
	for _, s := range rest {
		links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`,
			html.EscapeString(s.URL), html.EscapeString(streamLinkText(s.URL))))
		shown++
		if limit > 0 && shown >= limit {
			break
		}
		if shown >= config.MaxStreamsHardCap {
			break
		}
	}
	if len(links) > 0 {
		lines = append(lines, "📺 Смотреть: "+strings.Join(links, " | "))
	}
	lines = append(lines, fallbackLine(m))
	return lines
}

// streamLine — однострочная обертка для обратной совместимости формата:
// склеивает streamLines через \n.
func streamLine(m domain.Match, limit int) string {
	return strings.Join(streamLines(m, limit), "\n")
}
