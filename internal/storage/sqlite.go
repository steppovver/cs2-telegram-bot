package storage

import (
	"database/sql"
	"fmt"

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
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY
	);
	CREATE TABLE IF NOT EXISTS subscriptions (
		user_id INTEGER,
		team_name TEXT,
		UNIQUE(user_id, team_name),
		FOREIGN KEY (user_id) REFERENCES users(id)
	);`
	_, err := s.db.Exec(query)
	return err
}

func (s *Storage) Subscribe(userID int64, teamName string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT OR IGNORE INTO subscriptions (user_id, team_name) VALUES (?, ?)`, userID, teamName)
	return err
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

func (s *Storage) GetUsersByTeam(teamName string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT user_id FROM subscriptions WHERE team_name = ?`, teamName)
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

// GetAllSubscribedTeams возвращает уникальный список команд, на которые есть хотя бы одна подписка
func (s *Storage) GetAllSubscribedTeams() ([]string, error) {
	// Используем DISTINCT, чтобы не получать дубликаты
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
