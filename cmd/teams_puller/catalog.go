package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	teamsAPI     = "https://api.pandascore.co/csgo/teams"
	teamsPerPage = 100
	maxAttempts  = 3
	idChunk      = 500
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

// applyCatalog приводит teams/players к снимку API одной транзакцией.
// Пустой ответ не пишет ничего. Команды без игроков не добавляются;
// если строка уже есть, она остаётся, а состав снимается.
// Команды, которых нет в ответе, удаляются вместе с подписками.
func applyCatalog(ctx context.Context, db *sql.DB, teams []pandaScoreTeam) error {
	if len(teams) == 0 {
		return errors.New("каталог команд пуст, справочник не изменён")
	}

	byID := make(map[int64]pandaScoreTeam, len(teams))
	seen := make([]int64, 0, len(teams))
	for _, team := range teams {
		if _, ok := byID[team.ID]; !ok {
			seen = append(seen, team.ID)
		}
		byID[team.ID] = team
	}

	var rosterIDs []int64
	var roster []pandaScoreTeam
	for _, id := range seen {
		team := byID[id]
		if len(team.Players) == 0 {
			continue
		}
		rosterIDs = append(rosterIDs, id)
		roster = append(roster, team)
	}

	slog.Info("Применение каталога команд",
		slog.Int("всего_команд", len(seen)),
		slog.Int("с_составом", len(roster)),
		slog.Int("без_состава_пропущено", len(seen)-len(roster)))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DROP TABLE IF EXISTS catalog_seen`); err != nil {
		return fmt.Errorf("очистка catalog_seen: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS catalog_roster`); err != nil {
		return fmt.Errorf("очистка catalog_roster: %w", err)
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE catalog_seen (id INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("создание catalog_seen: %w", err)
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE catalog_roster (id INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("создание catalog_roster: %w", err)
	}
	if err := insertIDs(tx, "catalog_seen", seen); err != nil {
		return err
	}
	if err := insertIDs(tx, "catalog_roster", rosterIDs); err != nil {
		return err
	}

	teamStmt, err := tx.Prepare(`INSERT INTO teams (id, name) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name`)
	if err != nil {
		return fmt.Errorf("подготовка вставки команд: %w", err)
	}
	defer teamStmt.Close()

	playerStmt, err := tx.Prepare(`INSERT INTO players (id, team_id, name) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, team_id=excluded.team_id`)
	if err != nil {
		return fmt.Errorf("подготовка вставки игроков: %w", err)
	}
	defer playerStmt.Close()

	for _, team := range roster {
		if _, err := teamStmt.Exec(team.ID, team.Name); err != nil {
			return fmt.Errorf("вставка команды %d: %w", team.ID, err)
		}
	}

	res, err := tx.Exec(`DELETE FROM players WHERE team_id IN (SELECT id FROM catalog_roster)`)
	if err != nil {
		return fmt.Errorf("очистка обновляемых составов: %w", err)
	}
	cleared, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("число очищенных игроков: %w", err)
	}
	playersInserted := 0
	for _, team := range roster {
		for _, p := range team.Players {
			if _, err := playerStmt.Exec(p.ID, team.ID, p.Name); err != nil {
				return fmt.Errorf("вставка игрока %d: %w", p.ID, err)
			}
			playersInserted++
		}
	}
	slog.Info("Составы перезаписаны",
		slog.Int("команд", len(roster)),
		slog.Int64("удалено_старых_игроков", cleared),
		slog.Int("вставлено_игроков", playersInserted))
	res, err = tx.Exec(`DELETE FROM players WHERE team_id NOT IN (SELECT id FROM catalog_roster)`)
	if err != nil {
		return fmt.Errorf("удаление устаревших составов: %w", err)
	}
	stalePlayers, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("число устаревших игроков: %w", err)
	}
	if stalePlayers > 0 {
		slog.Info("Удалены устаревшие составы", slog.Int64("игроков", stalePlayers))
	}

	res, err = tx.Exec(`DELETE FROM teams WHERE id NOT IN (SELECT id FROM catalog_seen)`)
	if err != nil {
		return fmt.Errorf("удаление пропавших команд: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("число удалённых команд: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("коммит каталога: %w", err)
	}
	slog.Info("Каталог применён",
		slog.Int("команд_с_составом", len(roster)),
		slog.Int64("удалено_пропавших_команд", removed))
	fmt.Printf("Готово! Команд с составом: %d, удалено пропавших команд: %d\n", len(roster), removed)
	return nil
}

func insertIDs(tx *sql.Tx, table string, ids []int64) error {
	for start := 0; start < len(ids); start += idChunk {
		end := min(start+idChunk, len(ids))
		part := ids[start:end]
		placeholders := make([]string, len(part))
		args := make([]any, len(part))
		for i, id := range part {
			placeholders[i] = "(?)"
			args[i] = id
		}
		q := "INSERT INTO " + table + " (id) VALUES " + strings.Join(placeholders, ",")
		if _, err := tx.Exec(q, args...); err != nil {
			return fmt.Errorf("запись id в %s: %w", table, err)
		}
	}
	return nil
}
