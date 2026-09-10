package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/domain"

	_ "modernc.org/sqlite"
)

type Storage struct {
	db      *sql.DB
	teamsDB *sql.DB
}

func NewStorage(botDbPath, teamsDbPath string) (*Storage, error) {
	botDSN := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL", botDbPath)
	db, err := sql.Open("sqlite", botDSN)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия bot.db: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)

	db.SetConnMaxLifetime(time.Hour)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ошибка подключения к bot.db: %w", err)
	}

	// Подключение к teams.db в режиме read-only
	teamsDSN := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", teamsDbPath)
	teamsDB, err := sql.Open("sqlite", teamsDSN)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия teams.db: %w", err)
	}

	teamsDB.SetMaxOpenConns(10)
	teamsDB.SetMaxIdleConns(10)

	if err := teamsDB.Ping(); err != nil {
		return nil, fmt.Errorf("ошибка подключения к teams.db: %w", err)
	}

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

func (s *Storage) ProcessMatch(m domain.Match) (isNew bool, timeChanged bool, teamsChanged bool, oldTime time.Time, oldTeamA string, oldTeamB string, err error) {
	// Открываем транзакцию. При использовании WAL это безопасно сериализует запись
	tx, err := s.db.Begin()
	if err != nil {
		return false, false, false, time.Time{}, "", "", err
	}
	defer tx.Rollback() // Автоматически откатит изменения, если не вызван tx.Commit()

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

func (s *Storage) GetUpcomingUserMatches(subs []string) ([]domain.Match, error) {
	if len(subs) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(subs))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	inClause := strings.Join(placeholders, ", ")

	query := fmt.Sprintf(`
		SELECT id, team_a, team_b, begin_at 
		FROM matches 
		WHERE begin_at > ? 
		  AND (team_a COLLATE NOCASE IN (%s) OR team_b COLLATE NOCASE IN (%s))
		ORDER BY begin_at ASC
	`, inClause, inClause)

	args := make([]any, 0, 1+len(subs)*2)
	args = append(args, time.Now().Unix())
	for _, sub := range subs {
		args = append(args, sub)
	}
	for _, sub := range subs {
		args = append(args, sub)
	}

	rows, err := s.db.Query(query, args...)
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

func (s *Storage) GetTeamIDByName(name string) (string, error) {
	var id int
	err := s.teamsDB.QueryRow(`SELECT id FROM teams WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d", id), nil
}

func (s *Storage) GetTeamIDsByNames(names []string) (map[string]string, error) {
	if len(names) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		placeholders[i] = "?"
		args[i] = name
	}

	query := fmt.Sprintf(`SELECT id, name FROM teams WHERE name COLLATE NOCASE IN (%s)`, strings.Join(placeholders, ","))
	rows, err := s.teamsDB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err == nil {
			result[strings.ToLower(name)] = fmt.Sprintf("%d", id)
		}
	}
	return result, rows.Err()
}

func (s *Storage) Close() error {
	err1 := s.db.Close()
	err2 := s.teamsDB.Close()
	if err1 != nil {
		return err1
	}
	return err2
}
