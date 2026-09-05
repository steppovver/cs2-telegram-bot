package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"cs2bot/internal/api" // Замени на свое имя модуля, если нужно

	_ "modernc.org/sqlite"
)

type Storage struct {
	db *sql.DB
}

func NewStorage(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия БД: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ошибка подключения к БД: %w", err)
	}

	s := &Storage{db: db}
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
	-- Новая таблица для хранения состояния матчей
	CREATE TABLE IF NOT EXISTS matches (
		id INTEGER PRIMARY KEY,
		team_a TEXT,
		team_b TEXT,
		begin_at INTEGER -- Храним время в Unix timestamp (секунды)
	);`
	_, err := s.db.Exec(query)
	return err
}

// ProcessMatch сверяет матч с базой. Возвращает флаги, если матч новый, время изменено или соперник изменился.
func (s *Storage) ProcessMatch(m api.Match) (isNew bool, timeChanged bool, teamsChanged bool, oldTime time.Time, oldTeamA string, oldTeamB string, err error) {
	var dbTimeUnix int64
	var dbTeamA, dbTeamB string

	// Запрашиваем сразу и время, и команды
	err = s.db.QueryRow(`SELECT begin_at, team_a, team_b FROM matches WHERE id = ?`, m.ID).
		Scan(&dbTimeUnix, &dbTeamA, &dbTeamB)

	if err == sql.ErrNoRows {
		// Совпадений нет — это совершенно новый матч
		_, err = s.db.Exec(`INSERT INTO matches (id, team_a, team_b, begin_at) VALUES (?, ?, ?, ?)`,
			m.ID, m.TeamA, m.TeamB, m.Time.Unix())
		return true, false, false, time.Time{}, "", "", err
	} else if err != nil {
		return false, false, false, time.Time{}, "", "", err
	}

	timeChanged = dbTimeUnix != m.Time.Unix()
	teamsChanged = (dbTeamA != m.TeamA) || (dbTeamB != m.TeamB)

	// Если хоть что-то изменилось — обновляем БД
	if timeChanged || teamsChanged {
		_, err = s.db.Exec(`UPDATE matches SET begin_at = ?, team_a = ?, team_b = ? WHERE id = ?`,
			m.Time.Unix(), m.TeamA, m.TeamB, m.ID)

		return false, timeChanged, teamsChanged, time.Unix(dbTimeUnix, 0), dbTeamA, dbTeamB, err
	}

	// Матч есть и ничего не изменилось
	return false, false, false, time.Time{}, "", "", nil
}

// GetUpcomingUserMatches достает из локальной БД актуальные матчи для команд пользователя
func (s *Storage) GetUpcomingUserMatches(subs []string) ([]api.Match, error) {
	if len(subs) == 0 {
		return nil, nil
	}

	// Берем из БД только матчи, которые еще не начались
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

		// Фильтруем: оставляем только те матчи, где играет подписанная команда
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

// CleanOldMatches удаляет из базы матчи, которые начались более 24 часов назад
func (s *Storage) CleanOldMatches() {
	s.db.Exec(`DELETE FROM matches WHERE begin_at < ?`, time.Now().Add(-24*time.Hour).Unix())
}

// === СТАРЫЕ МЕТОДЫ ПОДПИСОК ОСТАЮТСЯ БЕЗ ИЗМЕНЕНИЙ ===

func (s *Storage) Subscribe(userID int64, teamName string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO subscriptions (user_id, team_name) VALUES (?, ?)`, userID, teamName)
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

func (s *Storage) Close() error {
	return s.db.Close()
}
