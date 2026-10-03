package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
)

type Client struct {
	apiKey     string
	httpClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
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
func (c *Client) queryChunk(ctx context.Context, q matchesQuery, merged map[int]domain.Match) error {
	for page := 1; ; page++ {
		pandaMatches, err := c.getMatchPage(ctx, q.url(page), page)
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
		// Короткая страница — дальше данных нет.
		if len(pandaMatches) < matchesPerPage {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

// getMatchPage выполняет GET с ретраями и декодирует страницу матчей.
func (c *Client) getMatchPage(ctx context.Context, url string, page int) ([]pandaMatch, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")

	var resp *http.Response
	var doErr error
	var requestSuccess bool

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, doErr = c.httpClient.Do(req)

		if doErr == nil {
			if resp.StatusCode == http.StatusOK {
				requestSuccess = true
				break
			}

			if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
				resp.Body.Close()
				return nil, fmt.Errorf("API client error: %d", resp.StatusCode)
			}
			resp.Body.Close()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}

	if doErr != nil {
		return nil, fmt.Errorf("ошибка сети при запросе страницы %d: %w", page, doErr)
	}
	if !requestSuccess {
		return nil, fmt.Errorf("превышено количество попыток запроса для страницы %d", page)
	}

	var pandaMatches []pandaMatch
	err = json.NewDecoder(resp.Body).Decode(&pandaMatches)
	resp.Body.Close()

	if err != nil {
		return nil, fmt.Errorf("ошибка парсинга страницы %d: %w", page, err)
	}
	return pandaMatches, nil
}
