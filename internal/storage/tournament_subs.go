package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"cs2bot/internal/domain"
)

const (
	// tournamentMenuGrace — сколько турнир виден в меню после окончания.
	tournamentMenuGrace = 24 * time.Hour
	// tournamentUnknownEndWindow — турнир без end_at считаем активным,
	// если он начался не раньше этого окна.
	tournamentUnknownEndWindow = 30 * 24 * time.Hour
	// tournamentCleanupGrace — через сколько после окончания удаляются
	// связи с командами и подписки. Запас на позднюю правку serie.end_at.
	tournamentCleanupGrace = 7 * 24 * time.Hour
	// tournamentCleanupUnknownEnd — то же для турниров без end_at.
	tournamentCleanupUnknownEnd = 60 * 24 * time.Hour
)

// activeTournamentCond — турнир идет, скоро начнется или закончился
// недавно. Параметры: endedAfter, startedAfter (unix).
const activeTournamentCond = `((t.end_at > 0 AND t.end_at >= ?) OR (t.end_at = 0 AND t.begin_at >= ?))`

func activeTournamentArgs(now time.Time) []any {
	return []any{now.Add(-tournamentMenuGrace).Unix(), now.Add(-tournamentUnknownEndWindow).Unix()}
}

// GetTournamentsForUser возвращает активные турниры, где играла хотя бы
// одна команда пользователя, плюс уже подписанные. По времени старта.
func (s *Storage) GetTournamentsForUser(userID int64) ([]domain.TournamentInfo, error) {
	args := []any{userID}
	args = append(args, activeTournamentArgs(time.Now())...)
	args = append(args, userID, userID)
	rows, err := s.db.Query(`
		SELECT t.id, t.name, t.begin_at, t.end_at,
			EXISTS (SELECT 1 FROM tournament_subscriptions ts WHERE ts.user_id = ? AND ts.tournament_id = t.id)
		FROM tournaments t
		WHERE `+activeTournamentCond+` AND (
			EXISTS (SELECT 1 FROM tournament_teams tt
				INNER JOIN subscriptions s ON s.team_id = tt.team_id
				WHERE s.user_id = ? AND tt.tournament_id = t.id)
			OR EXISTS (SELECT 1 FROM tournament_subscriptions ts WHERE ts.user_id = ? AND ts.tournament_id = t.id)
		)
		ORDER BY t.begin_at ASC, t.id ASC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("выборка турниров пользователя: %w", err)
	}
	defer rows.Close()

	var out []domain.TournamentInfo
	for rows.Next() {
		var t domain.TournamentInfo
		var begin, end int64
		var subscribed int
		if err := rows.Scan(&t.ID, &t.Name, &begin, &end, &subscribed); err != nil {
			return nil, fmt.Errorf("скан турнира: %w", err)
		}
		if begin > 0 {
			t.BeginAt = time.Unix(begin, 0)
		}
		if end > 0 {
			t.EndAt = time.Unix(end, 0)
		}
		t.Subscribed = subscribed != 0
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("выборка турниров пользователя: %w", err)
	}
	return out, nil
}

func (s *Storage) SubscribeTournament(userID int64, tournamentID int) error {
	return s.withUser(userID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT OR IGNORE INTO tournament_subscriptions (user_id, tournament_id) VALUES (?, ?)`,
			userID, tournamentID)
		return err
	})
}

func (s *Storage) UnsubscribeTournament(userID int64, tournamentID int) error {
	if _, err := s.db.Exec(`DELETE FROM tournament_subscriptions WHERE user_id = ? AND tournament_id = ?`,
		userID, tournamentID); err != nil {
		return fmt.Errorf("отписка от турнира %d: %w", tournamentID, err)
	}
	return nil
}

// HasTournamentSubscriptions сообщает, подписан ли пользователь хоть на один турнир.
func (s *Storage) HasTournamentSubscriptions(userID int64) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tournament_subscriptions WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return false, fmt.Errorf("проверка подписок на турниры: %w", err)
	}
	return n > 0, nil
}

func (s *Storage) GetUsersByTournamentID(tournamentID int) ([]int64, error) {
	rows, err := s.db.Query(`SELECT user_id FROM tournament_subscriptions WHERE tournament_id = ?`, tournamentID)
	if err != nil {
		return nil, fmt.Errorf("подписчики турнира %d: %w", tournamentID, err)
	}
	defer rows.Close()

	var users []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("скан подписчика турнира: %w", err)
		}
		users = append(users, id)
	}
	return users, rows.Err()
}

// GetActiveSubscribedTournamentIDs — серии для опроса API. Закончившиеся
// не опрашиваем, чтобы не тратить лимит.
func (s *Storage) GetActiveSubscribedTournamentIDs() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT ts.tournament_id
		FROM tournament_subscriptions ts
		INNER JOIN tournaments t ON t.id = ts.tournament_id
		WHERE `+activeTournamentCond, activeTournamentArgs(time.Now())...)
	if err != nil {
		return nil, fmt.Errorf("выборка подписанных турниров: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("скан подписанного турнира: %w", err)
		}
		ids = append(ids, strconv.Itoa(id))
	}
	return ids, rows.Err()
}

// GetTournamentView: true — по умолчанию показывать турнирные матчи.
func (s *Storage) GetTournamentView(userID int64) (bool, error) {
	var v int
	err := s.db.QueryRow(`SELECT tournament_view FROM user_settings WHERE user_id = ?`, userID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("чтение режима турнирных матчей: %w", err)
	}
	return v != 0, nil
}

func (s *Storage) SetTournamentView(userID int64, showAll bool) error {
	return s.withUser(userID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO user_settings (user_id, utc_offset, tournament_view)
			VALUES (?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET tournament_view = excluded.tournament_view`,
			userID, DefaultUTCOffset, boolInt(showAll))
		return err
	})
}

// CleanFinishedTournaments удаляет связи с командами и подписки давно
// закончившихся турниров. Сами строки tournaments не трогает.
func (s *Storage) CleanFinishedTournaments() {
	n, err := s.cleanFinishedTournaments(time.Now())
	if err != nil {
		slog.Error("Ошибка при очистке закончившихся турниров", slog.Any("error", err))
		return
	}
	if n > 0 {
		slog.Info("Очистка закончившихся турниров", slog.Int64("deleted", n))
	}
}

func (s *Storage) cleanFinishedTournaments(now time.Time) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()

	finished := `SELECT id FROM tournaments WHERE (end_at > 0 AND end_at < ?) OR (end_at = 0 AND begin_at < ?)`
	endedBefore := now.Add(-tournamentCleanupGrace).Unix()
	startedBefore := now.Add(-tournamentCleanupUnknownEnd).Unix()

	var deleted int64
	for _, table := range []string{"tournament_teams", "tournament_subscriptions"} {
		res, err := tx.Exec(`DELETE FROM `+table+` WHERE tournament_id IN (`+finished+`)`, endedBefore, startedBefore)
		if err != nil {
			return 0, fmt.Errorf("очистка %s: %w", table, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			deleted += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("коммит: %w", err)
	}
	return deleted, nil
}
