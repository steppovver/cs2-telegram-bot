package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"cs2bot/internal/storage"
)

const (
	teamsAPI     = "https://api.pandascore.co/csgo/teams"
	teamsPerPage = 100
	maxAttempts  = 3
)

// fetchTeams забирает каталог команд целиком. Пустая страница — конец.
// Сбойная страница повторяется до maxAttempts раз, как страницы матчей:
// сеть, 429 и 5xx ждут и пробуют снова, прочий 4xx сразу ошибка.
func fetchTeams(ctx context.Context, client *http.Client, token string) ([]pandaScoreTeam, error) {
	var all []pandaScoreTeam
	for page := 1; ; page++ {
		if page > 1 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		pageURL := fmt.Sprintf("%s?page[number]=%d&page[size]=%d", teamsAPI, page, teamsPerPage)
		var batch []pandaScoreTeam
		if err := getJSON(ctx, client, pageURL, token, &batch); err != nil {
			return nil, fmt.Errorf("страница %d: %w", page, err)
		}
		if len(batch) == 0 {
			fmt.Printf("Вернулся пустой ответ - все команды загружены\n")
			return all, nil
		}
		all = append(all, batch...)
		fmt.Printf("Страница %d загружена (%d команд)...\n", page, len(batch))
	}
}

func getJSON(ctx context.Context, client *http.Client, pageURL, token string, dest any) error {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return fmt.Errorf("создание запроса: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
		} else {
			if resp.StatusCode == http.StatusOK {
				decErr := json.NewDecoder(resp.Body).Decode(dest)
				resp.Body.Close()
				if decErr != nil {
					return fmt.Errorf("разбор JSON: %w", decErr)
				}
				return nil
			}
			status := resp.StatusCode
			resp.Body.Close()
			if status < 500 && status != http.StatusTooManyRequests {
				return fmt.Errorf("PandaScore статус %d", status)
			}
			lastErr = fmt.Errorf("PandaScore статус %d", status)
			slog.Warn("PandaScore: повтор страницы каталога",
				slog.Int("attempt", attempt),
				slog.Int("status", status))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("неизвестная ошибка")
	}
	return fmt.Errorf("превышено количество попыток: %w", lastErr)
}

func catalogTeams(teams []pandaScoreTeam) []storage.CatalogTeam {
	out := make([]storage.CatalogTeam, len(teams))
	for i, team := range teams {
		players := make([]storage.CatalogPlayer, len(team.Players))
		for j, p := range team.Players {
			players[j] = storage.CatalogPlayer{ID: p.ID, Name: p.Name}
		}
		out[i] = storage.CatalogTeam{ID: team.ID, Name: team.Name, Players: players}
	}
	return out
}
