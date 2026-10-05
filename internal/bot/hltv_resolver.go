package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"cs2bot/internal/domain"
)

// Параметры резолвера точных ссылок HLTV.
const (
	// hltvResolveLeadTime — за сколько до старта начинаем искать страницу матча.
	hltvResolveLeadTime = 12 * time.Hour
	// hltvResolveLookback — как долго после старта еще добираем ссылку
	// (идущие и только что сыгранные матчи показываются в «Счете»).
	hltvResolveLookback = 6 * time.Hour
	hltvResolveInterval = 5 * time.Minute
	// Пауза между запросами к поисковику: частые запросы приводят к капче.
	hltvResolvePause = 7 * time.Second
	// hltvNegativeTTL — через сколько повторять поиск, если страница не найдена.
	hltvNegativeTTL = time.Hour
	// hltvErrorRetry — через сколько повторять поиск после сетевой ошибки
	// (короче hltvNegativeTTL, но не в каждом цикле).
	hltvErrorRetry = 15 * time.Minute
	// hltvMaxConsecutiveErrors — столько ошибок подряд в одном цикле
	// считаем признаком проблем у поисковика и уходим в backoff.
	hltvMaxConsecutiveErrors = 3
	hltvBlockedBackoff       = 30 * time.Minute
)

var hltvMatchPathRe = regexp.MustCompile(`^/matches/(\d+)/([a-z0-9-]+)/?$`)

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

// hltvSlugEvent разбирает slug вида "teama-vs-teamb-event-name" и
// проверяет, что в нем ровно эти две команды (в любом порядке).
// Возвращает токены названия события из хвоста slug.
func hltvSlugEvent(slug string, teamA, teamB []string) ([]string, bool) {
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

// validateMatchURL проверяет, что кандидат — страница именно этого матча
// на HLTV, и возвращает канонический URL.
func validateMatchURL(candidate string, m domain.Match) (string, bool) {
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
	parts := hltvMatchPathRe.FindStringSubmatch(u.Path)
	if parts == nil {
		return "", false
	}
	event, ok := hltvSlugEvent(parts[2], teamTokens(m.TeamA), teamTokens(m.TeamB))
	if !ok || !eventMatchesTournament(event, m.Tournament) {
		return "", false
	}
	return "https://www.hltv.org/matches/" + parts[1] + "/" + parts[2], true
}

// resolveHLTVMatch ищет страницу матча. Пустая строка без ошибки — не нашли.
func resolveHLTVMatch(ctx context.Context, sp SearchProvider, m domain.Match) (string, error) {
	query := "site:hltv.org/matches " + m.TeamA + " vs " + m.TeamB
	results, err := sp.Search(ctx, query)
	if err != nil {
		return "", fmt.Errorf("поиск страницы матча %d: %w", m.ID, err)
	}
	for _, r := range results {
		if u, ok := validateMatchURL(r, m); ok {
			return u, nil
		}
	}
	return "", nil
}

// attachHLTV подставляет найденные ссылки в матчи (на месте). Нужен только
// для матчей, пришедших из API (события поллера): матчи из БД получают
// HLTVURL сразу в селектах storage. Нет ссылки — HLTVURL остается пустым
// и рендер использует поиск Google.
func (b *Bot) attachHLTV(matches []domain.Match) {
	if len(matches) == 0 {
		return
	}
	ids := make([]int, len(matches))
	for i, m := range matches {
		ids[i] = m.ID
	}
	urls, err := b.storage.GetHLTVURLs(ids)
	if err != nil {
		slog.Warn("Не удалось прочитать ссылки HLTV", slog.Any("error", err))
		return
	}
	for i := range matches {
		matches[i].HLTVURL = urls[matches[i].ID]
	}
}

// StartHLTVResolver — воркер, который за hltvResolveLeadTime до старта
// ищет точную страницу матча на HLTV и сохраняет ее в matches.hltv_url.
func (b *Bot) StartHLTVResolver(ctx context.Context) {
	if b.hltvSearch == nil {
		slog.Info("Резолвер ссылок HLTV выключен, используется поиск Google")
		return
	}
	slog.Info("Резолвер ссылок HLTV запущен",
		slog.Duration("lead", hltvResolveLeadTime), slog.Duration("interval", hltvResolveInterval))

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("Резолвер ссылок HLTV завершил работу")
			return
		case <-timer.C:
		}

		wait := hltvResolveInterval
		if b.runHLTVResolveCycle(ctx, time.Now(), hltvResolvePause) {
			wait = hltvBlockedBackoff
		}
		timer.Reset(wait)
	}
}

// runHLTVResolveCycle ищет страницы для матчей окна, у которых ссылки еще нет
// и прошлая попытка была давно (hltvNegativeTTL). Фильтр делает БД.
// Возвращает true, если поисковик заблокировал запрос или ошибается
// подряд (нужен backoff).
func (b *Bot) runHLTVResolveCycle(ctx context.Context, now time.Time, pause time.Duration) (blocked bool) {
	consecutiveErrors := 0
	todo, err := b.storage.GetMatchesForHLTVResolve(
		now.Add(-hltvResolveLookback), now.Add(hltvResolveLeadTime), now.Add(-hltvNegativeTTL))
	if err != nil {
		slog.Error("Ошибка выборки матчей для поиска HLTV", slog.Any("error", err))
		return false
	}

	for i, m := range todo {
		if i > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(pause):
			}
		}
		if ctx.Err() != nil {
			return false
		}

		found, err := resolveHLTVMatch(ctx, b.hltvSearch, m)
		if err != nil {
			if ctx.Err() != nil {
				return false
			}
			if errors.Is(err, ErrSearchBlocked) {
				slog.Warn("Поисковик заблокировал запросы HLTV, пауза",
					slog.Duration("backoff", hltvBlockedBackoff), slog.Any("error", err))
				return true
			}
			slog.Warn("Не удалось найти страницу матча на HLTV",
				slog.Int("match_id", m.ID), slog.Any("error", err))

			// Отметка попытки сдвинута так, чтобы матч вернулся в выборку
			// через hltvErrorRetry, а не в каждом цикле.
			retryMark := now.Add(-(hltvNegativeTTL - hltvErrorRetry))
			if err := b.storage.SetMatchHLTV(m.ID, "", retryMark); err != nil {
				slog.Error("Не удалось сохранить отметку попытки HLTV", slog.Int("match_id", m.ID), slog.Any("error", err))
			}
			consecutiveErrors++
			if consecutiveErrors >= hltvMaxConsecutiveErrors {
				slog.Warn("Поиск HLTV часто падает, пауза",
					slog.Int("errors", consecutiveErrors), slog.Duration("backoff", hltvBlockedBackoff))
				return true
			}
			continue
		}
		consecutiveErrors = 0

		// Пустая found с checkedAt = "не нашли", повтор через hltvNegativeTTL.
		if err := b.storage.SetMatchHLTV(m.ID, found, now); err != nil {
			slog.Error("Не удалось сохранить ссылку HLTV", slog.Int("match_id", m.ID), slog.Any("error", err))
			continue
		}
		slog.Info("Поиск страницы матча HLTV",
			slog.Int("match_id", m.ID),
			slog.String("teams", m.TeamA+" vs "+m.TeamB),
			slog.Bool("found", found != ""))
	}
	return false
}
