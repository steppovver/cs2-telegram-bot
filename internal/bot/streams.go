package bot

import (
	"fmt"
	"html"
	"net/url"
	"strings"

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

// streamLine возвращает строку с трансляцией для уведомления о старте.
// Если есть официальные стримы — прямые ссылки, иначе fallback на поиск.
func streamLine(m domain.Match) string {
	if len(m.Streams) > 0 {
		links := make([]string, 0, len(m.Streams))
		for _, s := range m.Streams {
			if strings.TrimSpace(s.URL) == "" {
				continue
			}
			links = append(links, fmt.Sprintf(`<a href="%s">%s</a>`,
				html.EscapeString(s.URL), streamLabel(s.URL)))
			if len(links) == 2 {
				break
			}
		}
		if len(links) > 0 {
			return "📺 Смотреть: " + strings.Join(links, " | ")
		}
	}
	return fmt.Sprintf(`📺 Найти трансляцию: <a href="%s">Twitch</a> | <a href="%s">YouTube</a>`,
		html.EscapeString(twitchSearchURL(m.TeamA, m.TeamB)),
		html.EscapeString(youtubeSearchURL(m.TeamA, m.TeamB)))
}
