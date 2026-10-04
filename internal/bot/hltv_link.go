package bot

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	"cs2bot/internal/domain"
)

// hltvMatchURL строит ссылку на профиль матча через DuckDuckGo !ducky:
// открывается сразу первый результат по site:hltv.org/matches.
// Дата (Oct 4) отсекает встречи тех же команд на прошлых турнирах.
// Пример: https://duckduckgo.com/?q=!ducky+site:hltv.org/matches+NAVI+vs+FaZe+BLAST
func hltvMatchURL(m domain.Match) string {
	q := "!ducky site:hltv.org/matches " + m.TeamA + " vs " + m.TeamB
	if t := strings.TrimSpace(m.Tournament); t != "" {
		q += " " + t
	}
	if !m.Time.IsZero() {
		q += " " + m.Time.UTC().Format("Jan 2")
	}
	return "https://duckduckgo.com/?q=" + url.QueryEscape(q)
}

// hltvMatchLine возвращает строку ссылки на профиль матча.
// TBD-команды — пустая строка, рендер ее пропускает.
func hltvMatchLine(m domain.Match) string {
	if m.TeamA == "TBD" || m.TeamB == "TBD" {
		return ""
	}
	return fmt.Sprintf(`📊 HLTV: <a href="%s">Профиль матча</a>`, html.EscapeString(hltvMatchURL(m)))
}
