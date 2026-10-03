package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"cs2bot/internal/domain"
)

const (
	// matchesAPI — базовый URL списка матчей CS.
	matchesAPI = "https://api.pandascore.co/csgo/matches"
	// teamIDsPerRequest — ID команд на запрос, чтобы не упереться в лимиты длины URL.
	teamIDsPerRequest = 50
	// matchesPerPage — размер страницы. Короткий ответ = последняя страница.
	matchesPerPage = 100
	// maxAttempts — попыток GET одной страницы до сдачи.
	maxAttempts = 3
	// defaultAPIRateLimit — лимит запросов в час, если не задан явно.
	defaultAPIRateLimit = 1000
)

type Client struct {
	apiKey     string
	httpClient *http.Client

	rateLimit int
	statsMu   sync.Mutex
	statsHour string
	statsUsed int
	// statsRemaining — минимальный остаток X-Rate-Limit-Remaining за час.
	// -1 = ответов пока не было.
	statsRemaining int
}

func NewClient(apiKey string, rateLimit int) *Client {
	if rateLimit <= 0 {
		rateLimit = defaultAPIRateLimit
	}
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		rateLimit:      rateLimit,
		statsRemaining: -1,
	}
}

// APIUsage возвращает статистику за текущий UTC-час: число запросов,
// минимальный остаток лимита (-1 если ответов еще не было) и сам лимит.
func (c *Client) APIUsage() (used, remaining, limit int) {
	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	return c.statsUsed, c.statsRemaining, c.rateLimit
}

// noteResponse учитывает один HTTP-ответ: счетчик часа и заголовок
// X-Rate-Limit-Remaining. Час — wall-clock UTC, как и сам лимит "в час".
func (c *Client) noteResponse(resp *http.Response) {
	remaining := -1
	if v := strings.TrimSpace(resp.Header.Get("X-Rate-Limit-Remaining")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			remaining = n
		}
	}

	c.statsMu.Lock()
	defer c.statsMu.Unlock()
	hour := time.Now().UTC().Format("2006-01-02-15")
	if hour != c.statsHour {
		c.statsHour = hour
		c.statsUsed = 0
		c.statsRemaining = -1
	}
	c.statsUsed++
	if remaining >= 0 && (c.statsRemaining < 0 || remaining < c.statsRemaining) {
		c.statsRemaining = remaining
	}
}

// matchesQuery — фильтр списка матчей. URL генерируется из полей,
// новых строк с query-параметрами в коде нет: новый статус для postponed —
// просто элемент Statuses.
type matchesQuery struct {
	OpponentIDs []string
	Statuses    []string
	Sort        string
	// Since ограничивает окно слева: матч старше — стоп, остаток страниц
	// тоже старый. Работает только с сортировкой от новых к старым.
	// nil = без ограничения.
	Since *time.Time
}

// url собирает URL страницы из фильтра.
func (q matchesQuery) url(page int) string {
	return fmt.Sprintf("%s?filter[opponent_id]=%s&filter[status]=%s&sort=%s&per_page=%d&page=%d",
		matchesAPI,
		strings.Join(q.OpponentIDs, ","),
		strings.Join(q.Statuses, ","),
		q.Sort,
		matchesPerPage,
		page)
}

func (c *Client) FetchMatchesByTeamIDs(ctx context.Context, teamIDs []string) ([]domain.Match, error) {
	return c.queryMatches(ctx, matchesQuery{
		OpponentIDs: teamIDs,
		Statuses:    []string{"not_started", "running"},
		Sort:        "begin_at",
	})
}

// FetchFinishedMatchesByTeamIDs забирает завершенные матчи команд за окно
// [since, now] для кнопки счета. Историю целиком не тянем: стоп при выходе
// страницы за окно.
func (c *Client) FetchFinishedMatchesByTeamIDs(ctx context.Context, teamIDs []string, since time.Time) ([]domain.Match, error) {
	return c.queryMatches(ctx, matchesQuery{
		OpponentIDs: teamIDs,
		Statuses:    []string{"finished"},
		Sort:        "-begin_at",
		Since:       &since,
	})
}

// queryMatches выполняет запрос по фильтру: чанкует команды (один матч может
// попасть в несколько чанков через общего соперника — дедуп по ID),
// листает страницы чанка до конца данных или выхода за окно Since.
func (c *Client) queryMatches(ctx context.Context, q matchesQuery) ([]domain.Match, error) {
	if len(q.OpponentIDs) == 0 {
		return nil, nil
	}

	merged := make(map[int]domain.Match)
	for start := 0; start < len(q.OpponentIDs); start += teamIDsPerRequest {
		end := start + teamIDsPerRequest
		if end > len(q.OpponentIDs) {
			end = len(q.OpponentIDs)
		}
		chunk := q
		chunk.OpponentIDs = q.OpponentIDs[start:end]
		if err := c.queryChunk(ctx, chunk, merged); err != nil {
			return nil, err
		}
	}

	allMatches := make([]domain.Match, 0, len(merged))
	for _, m := range merged {
		allMatches = append(allMatches, m)
	}
	return allMatches, nil
}

// queryChunk листает страницы одного чанка, складывая матчи в merged.
// Навигация — по ссылке rel="next" из Link-заголовка; короткая страница
// и выход за окно Since — страховки на случай отсутствия линка.
func (c *Client) queryChunk(ctx context.Context, q matchesQuery, merged map[int]domain.Match) error {
	nextURL := q.url(1)
	for page := 1; nextURL != ""; page++ {
		pandaMatches, next, err := c.getMatchPage(ctx, nextURL, page)
		if err != nil {
			return err
		}
		// Пустой ответ — конец данных.
		if len(pandaMatches) == 0 {
			return nil
		}

		for _, pm := range pandaMatches {
			if q.Since != nil && !pm.BeginAt.IsZero() && pm.BeginAt.Before(*q.Since) {
				return nil
			}
			if m, ok := mapPandaMatch(pm); ok {
				merged[m.ID] = m
			}
		}
		if next == "" || len(pandaMatches) < matchesPerPage {
			return nil
		}
		nextURL = next

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return nil
}

// getMatchPage выполняет GET готового URL страницы с ретраями, декодирует
// матчи и возвращает ссылку rel="next" из Link-заголовка ("" если дальше
// некуда). page нужен только для текстов ошибок.
func (c *Client) getMatchPage(ctx context.Context, pageURL string, page int) ([]pandaMatch, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	var resp *http.Response
	var doErr error
	var requestSuccess bool

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, doErr = c.httpClient.Do(req)

		if doErr == nil {
			c.noteResponse(resp)
			if resp.StatusCode == http.StatusOK {
				requestSuccess = true
				break
			}

			if resp.StatusCode == http.StatusTooManyRequests {
				slog.Warn("PandaScore 429: превышаем rate limit, ждем и повторяем",
					slog.Int("attempt", attempt),
					slog.Int("page", page))
			}
			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				resp.Body.Close()
				return nil, "", fmt.Errorf("API client error: %d", resp.StatusCode)
			}
			resp.Body.Close()
		}

		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}

	if doErr != nil {
		return nil, "", fmt.Errorf("ошибка сети при запросе страницы %d: %w", page, doErr)
	}
	if !requestSuccess {
		return nil, "", fmt.Errorf("превышено количество попыток запроса для страницы %d", page)
	}

	next := parseNextLink(resp.Header.Get("Link"))

	var pandaMatches []pandaMatch
	err = json.NewDecoder(resp.Body).Decode(&pandaMatches)
	resp.Body.Close()

	if err != nil {
		return nil, "", fmt.Errorf("ошибка парсинга страницы %d: %w", page, err)
	}
	return pandaMatches, next, nil
}

// parseNextLink достает URL rel="next" из Link-заголовка вида
// `<url1>; rel="last", <url2>; rel="next"`. Нет линка — пустая строка.
func parseNextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segments := strings.SplitN(strings.TrimSpace(part), ";", 2)
		if len(segments) != 2 {
			continue
		}
		if strings.TrimSpace(segments[1]) != `rel="next"` {
			continue
		}
		u := strings.TrimSpace(segments[0])
		u = strings.TrimPrefix(u, "<")
		u = strings.TrimSuffix(u, ">")
		return strings.TrimSpace(u)
	}
	return ""
}
