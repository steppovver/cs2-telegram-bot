package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/api"

	_ "modernc.org/sqlite"
)

type Storage struct {
	db      *sql.DB // Подключение к bot.db (пользователи, подписки, матчи)
	teamsDB *sql.DB // Подключение к teams.db (справочник команд и игроков)
}

type SearchedTeam struct {
	ID      int
	Name    string
	Players string
}

func NewStorage(botDbPath, teamsDbPath string) (*Storage, error) {
	// Подключаемся к основной БД
	db, err := sql.Open("sqlite", botDbPath)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия bot.db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ошибка подключения к bot.db: %w", err)
	}

	// Подключаемся к БД с командами
	teamsDB, err := sql.Open("sqlite", teamsDbPath)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия teams.db: %w", err)
	}
	if err := teamsDB.Ping(); err != nil {
		return nil, fmt.Errorf("ошибка подключения к teams.db: %w", err)
	}

	// Отладочный вывод количества команд
	var count int
	if err := teamsDB.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&count); err == nil {
		slog.Info("Подключение к teams.db", slog.Int("всего_команд", count))
	} else {
		slog.Error("Ошибка чтения из teams.db", slog.Any("error", err))
	}

	s := &Storage{
		db:      db,
		teamsDB: teamsDB,
	}

	if err := s.initTables(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Storage) initTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY);
	CREATE TABLE IF NOT EXISTS subscriptions (
		user_id INTEGER,
		team_name TEXT,
		UNIQUE(user_id, team_name)
	);
	CREATE TABLE IF NOT EXISTS matches (
		id INTEGER PRIMARY KEY,
		team_a TEXT,
		team_b TEXT,
		begin_at INTEGER,
		notified INTEGER DEFAULT 0
	);`
	if _, err := s.db.Exec(query); err != nil {
		return err
	}

	_, err := s.db.Exec(`ALTER TABLE matches ADD COLUMN notified INTEGER DEFAULT 0;`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("ошибка обновления структуры БД: %w", err)
	}

	return nil
}

func (s *Storage) ProcessMatch(m api.Match) (isNew bool, timeChanged bool, teamsChanged bool, oldTime time.Time, oldTeamA string, oldTeamB string, err error) {
	var dbTimeUnix int64
	var dbTeamA, dbTeamB string

	err = s.db.QueryRow(`SELECT begin_at, team_a, team_b FROM matches WHERE id = ?`, m.ID).
		Scan(&dbTimeUnix, &dbTeamA, &dbTeamB)

	if err == sql.ErrNoRows {
		_, err = s.db.Exec(`INSERT INTO matches (id, team_a, team_b, begin_at) VALUES (?, ?, ?, ?)`,
			m.ID, m.TeamA, m.TeamB, m.Time.Unix())
		return true, false, false, time.Time{}, "", "", err
	} else if err != nil {
		return false, false, false, time.Time{}, "", "", err
	}

	timeChanged = dbTimeUnix != m.Time.Unix()
	teamsChanged = (dbTeamA != m.TeamA) || (dbTeamB != m.TeamB)

	if timeChanged || teamsChanged {
		_, err = s.db.Exec(`UPDATE matches SET begin_at = ?, team_a = ?, team_b = ? WHERE id = ?`,
			m.Time.Unix(), m.TeamA, m.TeamB, m.ID)
		return false, timeChanged, teamsChanged, time.Unix(dbTimeUnix, 0), dbTeamA, dbTeamB, err
	}

	return false, false, false, time.Time{}, "", "", nil
}

func (s *Storage) GetUpcomingUserMatches(subs []string) ([]api.Match, error) {
	if len(subs) == 0 {
		return nil, nil
	}

	rows, err := s.db.Query(`SELECT id, team_a, team_b, begin_at FROM matches WHERE begin_at > ? ORDER BY begin_at ASC`, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []api.Match
	for rows.Next() {
		var m api.Match
		var unixTime int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime); err != nil {
			continue
		}

		for _, sub := range subs {
			if strings.EqualFold(m.TeamA, sub) || strings.EqualFold(m.TeamB, sub) {
				m.Time = time.Unix(unixTime, 0)
				matches = append(matches, m)
				break
			}
		}
	}
	return matches, nil
}

func (s *Storage) CleanOldMatches() {
	s.db.Exec(`DELETE FROM matches WHERE begin_at < ?`, time.Now().Add(-24*time.Hour).Unix())
}

func (s *Storage) Subscribe(userID int64, teamName string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO subscriptions (user_id, team_name) VALUES (?, ?)`, userID, teamName)
	return err
}

func (s *Storage) Unsubscribe(userID int64, teamName string) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE user_id = ? AND team_name = ?`, userID, teamName)
	return err
}

func (s *Storage) GetUsersByTeam(teamName string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT user_id FROM subscriptions WHERE team_name = ? COLLATE NOCASE`, teamName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		users = append(users, id)
	}
	return users, rows.Err()
}

func (s *Storage) GetUserSubscriptions(userID int64) ([]string, error) {
	rows, err := s.db.Query(`SELECT team_name FROM subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []string
	for rows.Next() {
		var team string
		if err := rows.Scan(&team); err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

func (s *Storage) GetAllSubscribedTeams() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT team_name FROM subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []string
	for rows.Next() {
		var team string
		if err := rows.Scan(&team); err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

func (s *Storage) GetMatchesForReminder() ([]api.Match, error) {
	now := time.Now().Unix()
	fiveMinsLater := now + (5 * 60)

	query := `SELECT id, team_a, team_b, begin_at 
	          FROM matches 
	          WHERE begin_at > ? AND begin_at <= ? AND notified = 0`

	rows, err := s.db.Query(query, now, fiveMinsLater)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []api.Match
	for rows.Next() {
		var m api.Match
		var unixTime int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		matches = append(matches, m)
	}
	return matches, nil
}

func (s *Storage) MarkMatchAsNotified(matchID int) error {
	_, err := s.db.Exec(`UPDATE matches SET notified = 1 WHERE id = ?`, matchID)
	return err
}

func (s *Storage) SearchTeams(query string) ([]SearchedTeam, error) {
	rows, err := s.teamsDB.Query(`
		SELECT t.id, t.name, GROUP_CONCAT(p.name, ', ') 
		FROM teams t 
		LEFT JOIN players p ON t.id = p.team_id 
		WHERE t.name LIKE ? COLLATE NOCASE
		GROUP BY t.id, t.name
		LIMIT 10
	`, "%"+query+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []SearchedTeam
	for rows.Next() {
		var t SearchedTeam
		var players sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &players); err != nil {
			continue
		}
		if players.Valid {
			t.Players = players.String
		}
		teams = append(teams, t)
	}
	return teams, nil
}

// Получение ID команды из БД (обратите внимание: используем s.teamsDB)
func (s *Storage) GetTeamIDByName(name string) (string, error) {
	var id int
	err := s.teamsDB.QueryRow(`SELECT id FROM teams WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", id), nil
}

func (s *Storage) Close() error {
	err1 := s.db.Close()
	err2 := s.teamsDB.Close()
	if err1 != nil {
		return err1
	}
	return err2
}
