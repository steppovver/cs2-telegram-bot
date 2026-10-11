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

// streamLinkText возвращает подпись ссылки: "канал (Платформа)".
// Twitch/Kick — канал из пути URL; YouTube — из кеша stream_channels
// (поле Channel, заполняется фоновым oEmbed-резолвером). Без имени —
// просто платформа.
func streamLinkText(s domain.MatchStream) string {
	platform := streamLabel(s.URL)
	channel := streamChannel(s.URL)
	if channel == "" {
		channel = truncateChannel(s.Channel)
	}
	if channel != "" {
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

// channelNoiseSuffixes — хвосты, которые снимаем при сравнении хендла
// Twitch/Kick с отображаемым именем YouTube (длинные первыми).
var channelNoiseSuffixes = []string{
	"counterstrike", "csgo", "cs2", "esports", "gaming",
	"official", "tv", "live", "channel", "cs",
}

// normalizeChannelName оставляет только [a-z0-9] в нижнем регистре.
func normalizeChannelName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// channelBaseForm снимает суффикс параллельного стрима (a–d) и шумовые хвосты.
func channelBaseForm(norm string) string {
	if len(norm) >= 5 {
		last := norm[len(norm)-1]
		if last >= 'a' && last <= 'd' {
			norm = norm[:len(norm)-1]
		}
	}
	for {
		stripped := false
		for _, suf := range channelNoiseSuffixes {
			if len(norm) > len(suf) && strings.HasSuffix(norm, suf) {
				norm = strings.TrimSuffix(norm, suf)
				stripped = true
				break
			}
		}
		if !stripped {
			break
		}
	}
	return norm
}

func channelNamesOverlap(a, b string) bool {
	if len(a) < 3 || len(b) < 3 {
		return false
	}
	return a == b || strings.Contains(a, b) || strings.Contains(b, a)
}

// channelsRelated — похожи ли имена каналов (Twitch ESLCS / ESLCSA и
// YouTube «ESL Counter-Strike»). Пустые и слишком короткие — false.
func channelsRelated(a, b string) bool {
	na, nb := normalizeChannelName(a), normalizeChannelName(b)
	if !channelNamesOverlap(na, nb) {
		ba, bb := channelBaseForm(na), channelBaseForm(nb)
		return channelNamesOverlap(ba, bb)
	}
	return true
}

// displayChannel — имя канала для сравнения: из URL (Twitch/Kick) или Channel.
func displayChannel(s domain.MatchStream) string {
	if ch := streamChannel(s.URL); ch != "" {
		return ch
	}
	return strings.TrimSpace(s.Channel)
}

// promoteRelatedYouTube поднимает неофициальные YouTube в official, если
// имя канала похоже на официальный Twitch/Kick. Если YouTube уже есть среди
// official — ничего не меняет.
func promoteRelatedYouTube(official, rest []domain.MatchStream) (outOfficial, outRest []domain.MatchStream) {
	for _, s := range official {
		if streamLabel(s.URL) == "YouTube" {
			return official, rest
		}
	}
	var officialNames []string
	for _, s := range official {
		if ch := displayChannel(s); ch != "" {
			officialNames = append(officialNames, ch)
		}
	}
	if len(officialNames) == 0 {
		return official, rest
	}

	outOfficial = append(outOfficial, official...)
	for _, s := range rest {
		if streamLabel(s.URL) != "YouTube" || strings.TrimSpace(s.Channel) == "" {
			outRest = append(outRest, s)
			continue
		}
		related := false
		for _, name := range officialNames {
			if channelsRelated(name, s.Channel) {
				related = true
				break
			}
		}
		if related {
			outOfficial = append(outOfficial, s)
		} else {
			outRest = append(outRest, s)
		}
	}
	return outOfficial, outRest
}

func fallbackLine(m domain.Match) string {
	return fmt.Sprintf(`🔍 Найти трансляцию: <a href="%s">Twitch</a> | <a href="%s">YouTube</a>`,
		html.EscapeString(twitchSearchURL(m.TeamA, m.TeamB)),
		html.EscapeString(youtubeSearchURL(m.TeamA, m.TeamB)))
}

// streamLines возвращает строки трансляций:
//   - "⭐ Официальная трансляция: ..." — official=true плюс похожие YouTube
//     (если среди official ещё нет YouTube);
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
	official, rest = promoteRelatedYouTube(official, rest)
	var lines []string
	if len(official) > 0 {
		links := make([]string, 0, len(official))
		for _, s := range official {
			links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`,
				html.EscapeString(s.URL), html.EscapeString(streamLinkText(s))))
		}
		lines = append(lines, "⭐ Официальная трансляция: "+strings.Join(links, " | "))
	}
	shown := 0
	links := make([]string, 0, len(rest))
	for _, s := range rest {
		links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`,
			html.EscapeString(s.URL), html.EscapeString(streamLinkText(s))))
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
