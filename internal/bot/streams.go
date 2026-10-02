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

func fallbackLine(m domain.Match) string {
	return fmt.Sprintf(`🔍 Найти трансляцию: <a href="%s">Twitch</a> | <a href="%s">YouTube</a>`,
		html.EscapeString(twitchSearchURL(m.TeamA, m.TeamB)),
		html.EscapeString(youtubeSearchURL(m.TeamA, m.TeamB)))
}

// streamLines возвращает строки трансляций: первая — официальные стримы
// (limit: 0 = все, иначе первые N, сверху hard-cap), вторая — всегда fallback
// на поиск. Если официальных нет — только fallback одной строкой.
func streamLines(m domain.Match, limit int) []string {
	limit = config.NormalizeMaxStreams(limit)
	links := make([]string, 0, len(m.Streams))
	for _, s := range m.Streams {
		if strings.TrimSpace(s.URL) == "" {
			continue
		}
		links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`,
			html.EscapeString(s.URL), streamLabel(s.URL)))
		if limit > 0 && len(links) >= limit {
			break
		}
		if len(links) >= config.MaxStreamsHardCap {
			break
		}
	}
	if len(links) == 0 {
		return []string{fallbackLine(m)}
	}
	return []string{
		"📺 Смотреть: " + strings.Join(links, " | "),
		fallbackLine(m),
	}
}

// streamLine — однострочная обертка для обратной совместимости формата:
// склеивает streamLines через \n.
func streamLine(m domain.Match, limit int) string {
	return strings.Join(streamLines(m, limit), "\n")
}
