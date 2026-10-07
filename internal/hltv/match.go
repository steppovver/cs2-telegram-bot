package hltv

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"cs2bot/internal/domain"
)

var matchPathRe = regexp.MustCompile(`^/matches/(\d+)/([a-z0-9-]+)/?$`)

// teamStopWords — слова, которые PandaScore добавляет к названиям команд,
// а HLTV в slug опускает ("Team Falcons" -> falcons).
var teamStopWords = map[string]bool{
	"team": true, "esports": true, "esport": true, "gaming": true, "club": true, "clan": true,
}

// nameTokens режет название на слова по пробелам/дефисам/подчеркиваниям,
// остальные знаки внутри слова выкидывает ("Virtus.pro" -> virtuspro).
func nameTokens(name string) []string {
	var tokens []string
	words := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return r == ' ' || r == '-' || r == '_'
	})
	for _, w := range words {
		var sb strings.Builder
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				sb.WriteRune(r)
			}
		}
		if sb.Len() > 0 {
			tokens = append(tokens, sb.String())
		}
	}
	return tokens
}

// teamTokens — токены команды без служебных слов. Если после фильтра
// ничего не осталось, возвращает все токены.
func teamTokens(name string) []string {
	all := nameTokens(name)
	var out []string
	for _, t := range all {
		if !teamStopWords[t] {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

// consumeTokens снимает с начала tokens слова, дающие в сумме want
// (сравнение без разделителей: natus+vincere == natusvincere).
// Возвращает остаток.
func consumeTokens(tokens, want []string) ([]string, bool) {
	target := strings.Join(want, "")
	if target == "" {
		return nil, false
	}
	acc := ""
	for i, t := range tokens {
		acc += t
		if acc == target {
			return tokens[i+1:], true
		}
		if !strings.HasPrefix(target, acc) {
			return nil, false
		}
	}
	return nil, false
}

// slugEvent разбирает slug вида "teama-vs-teamb-event-name" и
// проверяет, что в нем ровно эти две команды (в любом порядке).
// Возвращает токены названия события из хвоста slug.
func slugEvent(slug string, teamA, teamB []string) ([]string, bool) {
	tokens := strings.Split(slug, "-")
	vs := -1
	for i, t := range tokens {
		if t == "vs" {
			vs = i
			break
		}
	}
	if vs <= 0 || vs == len(tokens)-1 {
		return nil, false
	}
	left, right := tokens[:vs], tokens[vs+1:]

	for _, pair := range [2][2][]string{{teamA, teamB}, {teamB, teamA}} {
		rest, ok := consumeTokens(left, pair[0])
		if !ok || len(rest) != 0 {
			continue
		}
		event, ok := consumeTokens(right, pair[1])
		if !ok {
			continue
		}
		return event, true
	}
	return nil, false
}

// tournamentTokens — значимые слова названия турнира: без чисто годовых
// токенов (2026), которые HLTV в slug часто не пишет.
func tournamentTokens(name string) []string {
	var out []string
	for _, t := range nameTokens(name) {
		if len(t) == 4 && isDigits(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// eventMatchesTournament: хотя бы половина значимых слов турнира есть в
// названии события из slug. Защита от страницы старого матча тех же команд.
// Нет названия турнира — требований нет.
func eventMatchesTournament(event []string, tournament string) bool {
	sig := tournamentTokens(tournament)
	if len(sig) == 0 {
		return true
	}
	have := make(map[string]bool, len(event))
	for _, t := range event {
		have[t] = true
	}
	hit := 0
	for _, t := range sig {
		if have[t] {
			hit++
		}
	}
	return hit*2 >= len(sig)
}

// ValidateMatchURL проверяет, что кандидат — страница именно этого матча
// на HLTV, и возвращает канонический URL.
func ValidateMatchURL(candidate string, m domain.Match) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(candidate))
	if err != nil {
		return "", false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", false
	}
	if host := strings.ToLower(u.Hostname()); host != "hltv.org" && host != "www.hltv.org" {
		return "", false
	}
	parts := matchPathRe.FindStringSubmatch(u.Path)
	if parts == nil {
		return "", false
	}
	event, ok := slugEvent(parts[2], teamTokens(m.TeamA), teamTokens(m.TeamB))
	if !ok || !eventMatchesTournament(event, m.Tournament) {
		return "", false
	}
	return "https://www.hltv.org/matches/" + parts[1] + "/" + parts[2], true
}

// ResolveMatch ищет страницу матча. Пустая строка без ошибки — не нашли.
func ResolveMatch(ctx context.Context, sp SearchProvider, m domain.Match) (string, error) {
	query := "site:hltv.org/matches " + m.TeamA + " vs " + m.TeamB
	results, err := sp.Search(ctx, query)
	if err != nil {
		return "", fmt.Errorf("поиск страницы матча %d: %w", m.ID, err)
	}
	for _, r := range results {
		if u, ok := ValidateMatchURL(r, m); ok {
			return u, nil
		}
	}
	return "", nil
}

// googleLuckyURL строит ссылку на Google "Мне повезёт" (btnI=1):
// открывается сразу первый результат поиска.
func googleLuckyURL(query string) string {
	return "https://www.google.com/search?btnI=1&q=" + url.QueryEscape(query)
}

// MatchURL возвращает ссылку на профиль матча: точный URL из кеша
// резолвера (m.HLTVURL), а если его еще нет — Google "Мне повезёт":
// открывается сразу первый результат по site:hltv.org/matches.
// Дата (Oct 4) отсекает встречи тех же команд на прошлых турнирах.
func MatchURL(m domain.Match) string {
	if m.HLTVURL != "" {
		return m.HLTVURL
	}
	q := "site:hltv.org/matches " + m.TeamA + " vs " + m.TeamB
	if t := strings.TrimSpace(m.Tournament); t != "" {
		q += " " + t
	}
	if !m.Time.IsZero() {
		q += " " + m.Time.UTC().Format("Jan 2")
	}
	return googleLuckyURL(q)
}

// EventURL строит ссылку на страницу турнира через Google "Мне повезёт".
func EventURL(tournament string) string {
	return googleLuckyURL("site:hltv.org/events " + strings.TrimSpace(tournament))
}
