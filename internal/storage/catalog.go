package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

const catalogIDChunk = 500

// CatalogPlayer — игрок в снимке справочника команд.
type CatalogPlayer struct {
	ID   int64
	Name string
}

// CatalogTeam — команда в снимке справочника. Пустой Players значит
// состав не пришёл: строка teams, если уже есть, остаётся, игроки снимаются.
type CatalogTeam struct {
	ID      int64
	Name    string
	Players []CatalogPlayer
}

// CatalogApplyResult — сколько команд с составом записано и сколько
// пропавших команд удалено. Пустой снимок не меняет базу.
type CatalogApplyResult struct {
	WithRoster   int
	RemovedTeams int64
}

// ApplyCatalog приводит teams/players к снимку API одной транзакцией.
// Пустой ответ не пишет ничего. Команды без игроков не добавляются;
// если строка уже есть, она остаётся, а состав снимается.
// Команды, которых нет в ответе, удаляются вместе с подписками.
func ApplyCatalog(ctx context.Context, db *sql.DB, teams []CatalogTeam) (CatalogApplyResult, error) {
	if len(teams) == 0 {
		return CatalogApplyResult{}, errors.New("каталог команд пуст, справочник не изменён")
	}

	byID := make(map[int64]CatalogTeam, len(teams))
	seen := make([]int64, 0, len(teams))
	for _, team := range teams {
		if _, ok := byID[team.ID]; !ok {
			seen = append(seen, team.ID)
		}
		byID[team.ID] = team
	}

	var rosterIDs []int64
	var roster []CatalogTeam
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
		return CatalogApplyResult{}, fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DROP TABLE IF EXISTS catalog_seen`); err != nil {
		return CatalogApplyResult{}, fmt.Errorf("очистка catalog_seen: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE IF EXISTS catalog_roster`); err != nil {
		return CatalogApplyResult{}, fmt.Errorf("очистка catalog_roster: %w", err)
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE catalog_seen (id INTEGER PRIMARY KEY)`); err != nil {
		return CatalogApplyResult{}, fmt.Errorf("создание catalog_seen: %w", err)
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE catalog_roster (id INTEGER PRIMARY KEY)`); err != nil {
		return CatalogApplyResult{}, fmt.Errorf("создание catalog_roster: %w", err)
	}
	if err := insertCatalogIDs(tx, "catalog_seen", seen); err != nil {
		return CatalogApplyResult{}, err
	}
	if err := insertCatalogIDs(tx, "catalog_roster", rosterIDs); err != nil {
		return CatalogApplyResult{}, err
	}

	teamStmt, err := tx.Prepare(`INSERT INTO teams (id, name) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name`)
	if err != nil {
		return CatalogApplyResult{}, fmt.Errorf("подготовка вставки команд: %w", err)
	}
	defer teamStmt.Close()

	playerStmt, err := tx.Prepare(`INSERT INTO players (id, team_id, name) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, team_id=excluded.team_id`)
	if err != nil {
		return CatalogApplyResult{}, fmt.Errorf("подготовка вставки игроков: %w", err)
	}
	defer playerStmt.Close()

	for _, team := range roster {
		if _, err := teamStmt.Exec(team.ID, team.Name); err != nil {
			return CatalogApplyResult{}, fmt.Errorf("вставка команды %d: %w", team.ID, err)
		}
	}

	res, err := tx.Exec(`DELETE FROM players WHERE team_id IN (SELECT id FROM catalog_roster)`)
	if err != nil {
		return CatalogApplyResult{}, fmt.Errorf("очистка обновляемых составов: %w", err)
	}
	cleared, err := res.RowsAffected()
	if err != nil {
		return CatalogApplyResult{}, fmt.Errorf("число очищенных игроков: %w", err)
	}
	playersInserted := 0
	for _, team := range roster {
		for _, p := range team.Players {
			if _, err := playerStmt.Exec(p.ID, team.ID, p.Name); err != nil {
				return CatalogApplyResult{}, fmt.Errorf("вставка игрока %d: %w", p.ID, err)
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
		return CatalogApplyResult{}, fmt.Errorf("удаление устаревших составов: %w", err)
	}
	stalePlayers, err := res.RowsAffected()
	if err != nil {
		return CatalogApplyResult{}, fmt.Errorf("число устаревших игроков: %w", err)
	}
	if stalePlayers > 0 {
		slog.Info("Удалены устаревшие составы", slog.Int64("игроков", stalePlayers))
	}

	res, err = tx.Exec(`DELETE FROM teams WHERE id NOT IN (SELECT id FROM catalog_seen)`)
	if err != nil {
		return CatalogApplyResult{}, fmt.Errorf("удаление пропавших команд: %w", err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return CatalogApplyResult{}, fmt.Errorf("число удалённых команд: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return CatalogApplyResult{}, fmt.Errorf("коммит каталога: %w", err)
	}
	slog.Info("Каталог применён",
		slog.Int("команд_с_составом", len(roster)),
		slog.Int64("удалено_пропавших_команд", removed))
	return CatalogApplyResult{WithRoster: len(roster), RemovedTeams: removed}, nil
}

func insertCatalogIDs(tx *sql.Tx, table string, ids []int64) error {
	for start := 0; start < len(ids); start += catalogIDChunk {
		end := min(start+catalogIDChunk, len(ids))
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
