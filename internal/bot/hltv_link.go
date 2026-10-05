package bot

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	"cs2bot/internal/domain"
)

// googleLuckyURL строит ссылку на Google "Мне повезёт" (btnI=1):
// открывается сразу первый результат поиска.
func googleLuckyURL(query string) string {
	return "https://www.google.com/search?btnI=1&q=" + url.QueryEscape(query)
}

// hltvMatchURL строит ссылку на профиль матча через Google "Мне повезёт":
// открывается сразу первый результат по site:hltv.org/matches.
// Дата (Oct 4) отсекает встречи тех же команд на прошлых турнирах.
// Пример: https://www.google.com/search?btnI=1&q=site:hltv.org/matches+NAVI+vs+FaZe+BLAST
func hltvMatchURL(m domain.Match) string {
	q := "site:hltv.org/matches " + m.TeamA + " vs " + m.TeamB
	if t := strings.TrimSpace(m.Tournament); t != "" {
		q += " " + t
	}
	if !m.Time.IsZero() {
		q += " " + m.Time.UTC().Format("Jan 2")
	}
	return googleLuckyURL(q)
}

// hltvMatchLine возвращает строку ссылки на профиль матча.
// TBD-команды — пустая строка, рендер ее пропускает.
func hltvMatchLine(m domain.Match) string {
	if m.TeamA == "TBD" || m.TeamB == "TBD" {
		return ""
	}
	return fmt.Sprintf(`📊 HLTV: <a href="%s">Профиль матча</a>`, html.EscapeString(hltvMatchURL(m)))
}
