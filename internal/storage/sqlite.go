package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs2bot/internal/domain"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS teams (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS players (
	id INTEGER PRIMARY KEY,
	team_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE
);
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
);
CREATE INDEX IF NOT EXISTS idx_teams_name ON teams(name);
CREATE INDEX IF NOT EXISTS idx_players_team_id ON players(team_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_team ON subscriptions(team_name);
CREATE INDEX IF NOT EXISTS idx_subscriptions_user ON subscriptions(user_id);
CREATE INDEX IF NOT EXISTS idx_matches_begin_at ON matches(begin_at);
`

type Storage struct {
	db *sql.DB
}

func Open(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия БД: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ошибка подключения к БД: %w", err)
	}

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("ошибка включения внешних ключей: %w", err)
	}

	return db, nil
}

func InitSchema(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	_, err := db.Exec(`ALTER TABLE matches ADD COLUMN notified INTEGER DEFAULT 0;`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("ошибка обновления структуры БД: %w", err)
	}

	return nil
}

func NewStorage(dbPath string) (*Storage, error) {
	db, err := Open(dbPath)
	if err != nil {
		return nil, err
	}

	s := &Storage{db: db}

	if err := InitSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	if err := s.migrateLegacyTeamsDB(dbPath); err != nil {
		slog.Warn("Не удалось перенести данные из teams.db", slog.Any("error", err))
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&count); err == nil {
		slog.Info("Подключение к БД успешно", slog.Int("всего_команд", count))
	} else {
		slog.Error("Ошибка чтения команд из БД", slog.Any("error", err))
	}

	return s, nil
}

func (s *Storage) migrateLegacyTeamsDB(dbPath string) error {
	legacyPath := filepath.Join(filepath.Dir(dbPath), "teams.db")
	if _, err := os.Stat(legacyPath); err != nil {
		return nil
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	if _, err := s.db.Exec(`ATTACH DATABASE ? AS legacy`, legacyPath); err != nil {
		return err
	}
	defer s.db.Exec(`DETACH DATABASE legacy`)

	if _, err := s.db.Exec(`INSERT OR IGNORE INTO teams SELECT * FROM legacy.teams`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO players SELECT * FROM legacy.players`); err != nil {
		return err
	}

	slog.Info("Данные команд перенесены из teams.db")
	return nil
}

func (s *Storage) ProcessMatch(m domain.Match) (isNew bool, timeChanged bool, teamsChanged bool, oldTime time.Time, oldTeamA string, oldTeamB string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, false, false, time.Time{}, "", "", err
	}
	defer tx.Rollback()

	var dbTimeUnix int64
	var dbTeamA, dbTeamB string

	err = tx.QueryRow(`SELECT begin_at, team_a, team_b FROM matches WHERE id = ?`, m.ID).
		Scan(&dbTimeUnix, &dbTeamA, &dbTeamB)

	if err == sql.ErrNoRows {
		_, err = tx.Exec(`INSERT INTO matches (id, team_a, team_b, begin_at) VALUES (?, ?, ?, ?)`,
			m.ID, m.TeamA, m.TeamB, m.Time.Unix())
		if err != nil {
			return false, false, false, time.Time{}, "", "", err
		}
		tx.Commit()
		return true, false, false, time.Time{}, "", "", nil
	} else if err != nil {
		return false, false, false, time.Time{}, "", "", err
	}

	timeChanged = dbTimeUnix != m.Time.Unix()
	teamsChanged = (dbTeamA != m.TeamA) || (dbTeamB != m.TeamB)

	if timeChanged || teamsChanged {
		_, err = tx.Exec(`UPDATE matches SET begin_at = ?, team_a = ?, team_b = ? WHERE id = ?`,
			m.Time.Unix(), m.TeamA, m.TeamB, m.ID)
		if err != nil {
			return false, false, false, time.Time{}, "", "", err
		}
	}

	tx.Commit()
	return false, timeChanged, teamsChanged, time.Unix(dbTimeUnix, 0), dbTeamA, dbTeamB, nil
}

func (s *Storage) GetUpcomingUserMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at
		FROM matches m
		INNER JOIN subscriptions s
			ON (m.team_a = s.team_name COLLATE NOCASE OR m.team_b = s.team_name COLLATE NOCASE)
		WHERE s.user_id = ? AND m.begin_at > ?
		ORDER BY m.begin_at ASC
	`, userID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []domain.Match
	for rows.Next() {
		var m domain.Match
		var unixTime int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *Storage) CleanOldMatches() {
	_, err := s.db.Exec(`DELETE FROM matches WHERE begin_at < ?`, time.Now().Add(-24*time.Hour).Unix())
	if err != nil {
		slog.Error("Ошибка при очистке старых матчей", slog.Any("error", err))
	}
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

func (s *Storage) GetUsersByTeams(teamA, teamB string) ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT user_id
		FROM subscriptions
		WHERE team_name COLLATE NOCASE IN (?, ?)
	`, teamA, teamB)
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

func (s *Storage) GetUserSubscriptionTeams(userID int64) ([]domain.TeamInfo, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name
		FROM subscriptions s
		INNER JOIN teams t ON t.name = s.team_name COLLATE NOCASE
		WHERE s.user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []domain.TeamInfo
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		teams = append(teams, domain.TeamInfo{
			ID:   fmt.Sprintf("%d", id),
			Name: name,
		})
	}
	return teams, rows.Err()
}

func (s *Storage) GetSubscribedTeamIDs() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT t.id
		FROM subscriptions s
		INNER JOIN teams t ON t.name = s.team_name COLLATE NOCASE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, fmt.Sprintf("%d", id))
	}
	return ids, rows.Err()
}

func (s *Storage) GetMatchesForReminder() ([]domain.Match, error) {
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

	var matches []domain.Match
	for rows.Next() {
		var m domain.Match
		var unixTime int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *Storage) MarkMatchAsNotified(matchID int) error {
	_, err := s.db.Exec(`UPDATE matches SET notified = 1 WHERE id = ?`, matchID)
	return err
}

func (s *Storage) SearchTeams(query string) ([]domain.SearchedTeam, error) {
	rows, err := s.db.Query(`
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

	var teams []domain.SearchedTeam
	for rows.Next() {
		var t domain.SearchedTeam
		var players sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &players); err != nil {
			continue
		}
		if players.Valid {
			t.Players = players.String
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (s *Storage) Close() error {
	return s.db.Close()
}
