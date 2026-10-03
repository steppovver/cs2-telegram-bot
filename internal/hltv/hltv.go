// Package hltv резолвит точную ссылку на профиль матча на HLTV
// (https://www.hltv.org/matches/<id>/<slug>).
//
// PandaScore ID матча HLTV не отдает, поэтому ищем ссылку скрапингом
// страниц /matches (предстоящие/идущие) и /results (завершенные):
// ссылка вида falcons-vs-tyloo-esl-pro-league-season-24 содержит
// токены обеих команд, ближайшая по дате — наш матч.
package hltv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ErrNotFound — на страницах HLTV подходящего матча нет
// (HLTV его еще не добавил или пара не сматчилась).
var ErrNotFound = errors.New("hltv: матч не найден")

var (
	matchesPageURL = "https://www.hltv.org/matches"
	resultsPageURL = "https://www.hltv.org/results"
	baseURL        = "https://www.hltv.org"

	httpClient = &http.Client{Timeout: 20 * time.Second}

	userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// Backoff возвращает задержку до следующей попытки по номеру
// текущей попытки (1-based): 1ч, 2ч, дальше потолок 3ч.
func Backoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Hour
	case attempts == 2:
		return 2 * time.Hour
	default:
		return 3 * time.Hour
	}
}

// genericTokens — шумовые слова в именах команд, которых нет в слаге HLTV.
var genericTokens = map[string]bool{
	"team": true, "esports": true, "esport": true,
	"gaming": true, "club": true, "gg": true,
}

// teamTokens режет имя команды на значимые токены:
// "Team Falcons" -> ["falcons"], "BC.Game Esports" -> ["bc" "game"].
func teamTokens(name string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(tok) < 2 || genericTokens[tok] {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// slugTokens режет слаг ссылки на множество токенов.
func slugTokens(slug string) map[string]bool {
	set := make(map[string]bool)
	for _, tok := range strings.FieldsFunc(strings.ToLower(slug), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if tok != "" {
			set[tok] = true
		}
	}
	return set
}

// containsAll проверяет, что все токены команды есть в слаге.
// Порядок не важен: falcons-vs-tyloo и tyloo-vs-falcons — оба подходят.
func containsAll(slug map[string]bool, tokens []string) bool {
	for _, t := range tokens {
		if !slug[t] {
			return false
		}
	}
	return true
}

type candidate struct {
	href    string
	date    time.Time
	hasDate bool
}

var (
	// Ссылки на матчи исключают навигацию (/matches без ID не подходит).
	anchorRe = regexp.MustCompile(`<a[^>]+href="(/matches/\d+/[^"#?]+)"`)
	// Заголовки групп в /results: "Results for October 3rd 2026".
	dateRe = regexp.MustCompile(`Results for ([A-Za-z]+ \d{1,2})(?:st|nd|rd|th)? (\d{4})`)
	// Один проход по документу в порядке следования: заголовок или ссылка.
	combinedRe = regexp.MustCompile(`Results for [A-Za-z]+ \d{1,2}(?:st|nd|rd|th)? \d{4}|<a[^>]+href="(/matches/\d+/[^"#?]+)"`)
)

// parseResultsDate разбирает "October 3 2026" (ординалы уже срезаны regex).
func parseResultsDate(monthDay, year string) (time.Time, bool) {
	t, err := time.Parse("January 2 2006", monthDay+" "+year)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// parseCandidates собирает ссылки на матчи, привязывая к дате
// ближайшего заголовка выше. Ссылки до первого заголовка — без даты.
func parseCandidates(page string) []candidate {
	var out []candidate
	seen := make(map[string]bool)
	var cur time.Time
	hasCur := false
	for _, m := range combinedRe.FindAllStringSubmatch(page, -1) {
		if m[1] == "" {
			if d := dateRe.FindStringSubmatch(m[0]); d != nil {
				if t, ok := parseResultsDate(d[1], d[2]); ok {
					cur, hasCur = t, true
				}
			}
			continue
		}
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, candidate{href: m[1], date: cur, hasDate: hasCur})
	}
	return out
}

// slugOf вырезает слаг из href вида /matches/2398717/falcons-vs-tyloo-....
func slugOf(href string) string {
	parts := strings.Split(strings.Trim(href, "/"), "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// dateTolerance — макс. расхождение даты HLTV и begin_at: день гранулярный,
// плюс сдвиги поясов. Дальше — чужой матч с той же парой.
const dateTolerance = 48 * time.Hour

// findBest выбирает ссылку, где слаг содержит токены обеих команд.
// Из датированных — ближайшую по дате в допуске, иначе первую без даты.
func findBest(cands []candidate, teamA, teamB string, beginAt time.Time) (string, bool) {
	ta, tb := teamTokens(teamA), teamTokens(teamB)
	if len(ta) == 0 || len(tb) == 0 {
		return "", false
	}
	type hit struct {
		href string
		date time.Time
	}
	var dated, dateless []hit
	for _, c := range cands {
		if !containsAll(slugTokens(slugOf(c.href)), ta) {
			continue
		}
		if !containsAll(slugTokens(slugOf(c.href)), tb) {
			continue
		}
		if c.hasDate {
			dated = append(dated, hit{c.href, c.date})
		} else {
			dateless = append(dateless, hit{c.href, time.Time{}})
		}
	}
	if len(dated) > 0 && !beginAt.IsZero() {
		best := -1
		var bestDiff time.Duration
		for i, h := range dated {
			d := h.date.Sub(beginAt)
			if d < 0 {
				d = -d
			}
			if best == -1 || d < bestDiff {
				best, bestDiff = i, d
			}
		}
		if bestDiff <= dateTolerance {
			return dated[best].href, true
		}
		return "", false
	}
	if len(dateless) > 0 {
		return dateless[0].href, true
	}
	if len(dated) > 0 {
		return dated[0].href, true
	}
	return "", false
}

func fetchPage(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("hltv: запрос %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("hltv: %s вернул %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("hltv: чтение %s: %w", url, err)
	}
	return string(b), nil
}

// Resolve ищет профиль матча на HLTV. Порядок страниц — по статусу:
// finished сначала смотрят в /results, остальные — в /matches.
// Возвращает абсолютный URL или ErrNotFound.
func Resolve(ctx context.Context, teamA, teamB string, beginAt time.Time, status string) (string, error) {
	pages := []string{matchesPageURL, resultsPageURL}
	if status == "finished" || status == "post_match" {
		pages = []string{resultsPageURL, matchesPageURL}
	}
	parsedAny := false
	var lastErr error
	for _, url := range pages {
		page, err := fetchPage(ctx, url)
		if err != nil {
			lastErr = err
			continue
		}
		parsedAny = true
		if href, ok := findBest(parseCandidates(page), teamA, teamB, beginAt); ok {
			return baseURL + href, nil
		}
	}
	if !parsedAny && lastErr != nil {
		return "", lastErr
	}
	return "", ErrNotFound
}
