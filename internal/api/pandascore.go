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

type pandaMatch struct {
	ID        int       `json:"id"`
	BeginAt   time.Time `json:"begin_at"`
	Status    string    `json:"status"`
	Opponents []struct {
		Opponent struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"opponent"`
	} `json:"opponents"`
}

func (c *Client) FetchMatchesByTeamIDs(ctx context.Context, teamIDs []string) ([]domain.Match, error) {
	if len(teamIDs) == 0 {
		return nil, nil
	}

	// Чанкуем ID команд, чтобы не упереться в лимиты длины URL / API.
	// Один матч может попасть в несколько чанков (общий соперник) —
	// дедуплицируем по ID матча.
	const teamIDsPerRequest = 50
	merged := make(map[int]domain.Match)
	for start := 0; start < len(teamIDs); start += teamIDsPerRequest {
		end := start + teamIDsPerRequest
		if end > len(teamIDs) {
			end = len(teamIDs)
		}
		chunk, err := c.fetchMatchesChunk(ctx, teamIDs[start:end])
		if err != nil {
			return nil, err
		}
		for _, m := range chunk {
			merged[m.ID] = m
		}
	}

	allMatches := make([]domain.Match, 0, len(merged))
	for _, m := range merged {
		allMatches = append(allMatches, m)
	}
	return allMatches, nil
}

func (c *Client) fetchMatchesChunk(ctx context.Context, teamIDs []string) ([]domain.Match, error) {
	joinedIDs := strings.Join(teamIDs, ",")
	var allMatches []domain.Match
	page := 1

	for {
		url := fmt.Sprintf("https://api.pandascore.co/csgo/matches?filter[opponent_id]=%s&filter[status]=not_started,running&sort=begin_at&per_page=100&page=%d", joinedIDs, page)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")

		var resp *http.Response
		var doErr error
		var requestSuccess bool

		// Внутренний цикл ретраев для одной страницы
		for attempt := 1; attempt <= 3; attempt++ {
			resp, doErr = c.httpClient.Do(req)

			if doErr == nil {
				if resp.StatusCode == http.StatusOK {
					requestSuccess = true
					break // Успешно, выходим из цикла ретраев
				}

				if resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
					resp.Body.Close()
					return nil, fmt.Errorf("API client error: %d", resp.StatusCode)
				}
				resp.Body.Close() // Закрываем перед следующей попыткой
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

		// Декодируем текущую страницу
		var pandaMatches []pandaMatch
		err = json.NewDecoder(resp.Body).Decode(&pandaMatches)
		resp.Body.Close() // Обязательно закрываем тело сразу после декодирования

		if err != nil {
			return nil, fmt.Errorf("ошибка парсинга страницы %d: %w", page, err)
		}

		// Если массив пустой, значит мы достигли конца данных
		if len(pandaMatches) == 0 {
			break
		}

		// Маппинг данных из API в доменную модель (без изменений)
		for _, pm := range pandaMatches {
			if pm.Status == "canceled" {
				continue
			}
			// begin_at=null в API -> zero time: такой матч нельзя показать
			// и напомнить о нем, пропускаем до появления времени.
			if pm.BeginAt.IsZero() {
				continue
			}

			teamA, teamB := "TBD", "TBD"
			teamAID, teamBID := 0, 0

			if len(pm.Opponents) > 0 {
				teamA = pm.Opponents[0].Opponent.Name
				teamAID = pm.Opponents[0].Opponent.ID
			}
			if len(pm.Opponents) > 1 {
				teamB = pm.Opponents[1].Opponent.Name
				teamBID = pm.Opponents[1].Opponent.ID
			}

			if teamAID == 0 && teamBID == 0 {
				continue
			}

			allMatches = append(allMatches, domain.Match{
				ID:      pm.ID,
				TeamA:   teamA,
				TeamB:   teamB,
				TeamAID: teamAID,
				TeamBID: teamBID,
				Time:    pm.BeginAt,
				Status:  pm.Status,
			})
		}

		// Если API вернуло меньше элементов, чем размер страницы,
		// значит следующей страницы точно нет — экономим один HTTP запрос
		if len(pandaMatches) < 100 {
			break
		}

		page++
	}

	return allMatches, nil
}
