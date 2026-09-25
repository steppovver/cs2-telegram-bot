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
	user_id INTEGER NOT NULL,
	team_id INTEGER NOT NULL,
	PRIMARY KEY (user_id, team_id),
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
	FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS matches (
	id INTEGER PRIMARY KEY,
	team_a TEXT,
	team_b TEXT,
	team_a_id INTEGER,
	team_b_id INTEGER,
	begin_at INTEGER,
	notified INTEGER DEFAULT 0,
	status TEXT
);
CREATE INDEX IF NOT EXISTS idx_teams_name ON teams(name);
CREATE INDEX IF NOT EXISTS idx_players_team_id ON players(team_id);
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

	alters := []string{
		`ALTER TABLE matches ADD COLUMN notified INTEGER DEFAULT 0;`,
		`ALTER TABLE matches ADD COLUMN team_a_id INTEGER;`,
		`ALTER TABLE matches ADD COLUMN team_b_id INTEGER;`,
		`ALTER TABLE matches ADD COLUMN status TEXT;`,
	}
	for _, q := range alters {
		_, err := db.Exec(q)
		if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("ошибка обновления структуры БД: %w", err)
		}
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

	if err := s.migrateSubscriptionsToTeamID(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ошибка миграции подписок: %w", err)
	}

	if err := s.backfillMatchTeamIDs(); err != nil {
		slog.Warn("Не удалось заполнить ID команд в матчах", slog.Any("error", err))
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&count); err == nil {
		slog.Info("Подключение к БД успешно", slog.Int("всего_команд", count))
	} else {
		slog.Error("Ошибка чтения команд из БД", slog.Any("error", err))
	}

	return s, nil
}

func (s *Storage) tableColumns(table string) (map[string]bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
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

func (s *Storage) migrateSubscriptionsToTeamID() error {
	cols, err := s.tableColumns("subscriptions")
	if err != nil {
		return err
	}
	if !cols["team_name"] {
		_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_subscriptions_team ON subscriptions(team_id)`)
		return err
	}

	var before int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&before); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		CREATE TABLE subscriptions_new (
			user_id INTEGER NOT NULL,
			team_id INTEGER NOT NULL,
			PRIMARY KEY (user_id, team_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE
		)
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO subscriptions_new (user_id, team_id)
		SELECT DISTINCT s.user_id, t.id
		FROM subscriptions s
		INNER JOIN teams t ON t.name = s.team_name COLLATE NOCASE
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`DROP TABLE subscriptions`); err != nil {
		return err
	}
	if _, err := tx.Exec(`ALTER TABLE subscriptions_new RENAME TO subscriptions`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_subscriptions_team ON subscriptions(team_id)`); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	var after int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&after); err != nil {
		return err
	}
	slog.Info("Подписки переведены на team_id",
		slog.Int("было", before),
		slog.Int("стало", after),
		slog.Int("без_совпадения", before-after),
	)
	return nil
}

func (s *Storage) backfillMatchTeamIDs() error {
	_, err := s.db.Exec(`
		UPDATE matches
		SET team_a_id = (
			SELECT id FROM teams WHERE name = matches.team_a COLLATE NOCASE LIMIT 1
		)
		WHERE team_a_id IS NULL OR team_a_id = 0
	`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		UPDATE matches
		SET team_b_id = (
			SELECT id FROM teams WHERE name = matches.team_b COLLATE NOCASE LIMIT 1
		)
		WHERE team_b_id IS NULL OR team_b_id = 0
	`)
	return err
}

func (s *Storage) ProcessMatch(m domain.Match) (isNew bool, timeChanged bool, teamsChanged bool, statusChanged bool, oldTime time.Time, oldTeamA string, oldTeamB string, oldStatus string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}
	defer tx.Rollback()

	var dbTimeUnix int64
	var dbTeamA, dbTeamB, dbStatus string

	err = tx.QueryRow(`SELECT begin_at, team_a, team_b, COALESCE(status, '') FROM matches WHERE id = ?`, m.ID).
		Scan(&dbTimeUnix, &dbTeamA, &dbTeamB, &dbStatus)

	if err == sql.ErrNoRows {
		slog.Debug("ProcessMatch new",
			slog.Int("match_id", m.ID),
			slog.String("teams", fmt.Sprintf("%s vs %s", m.TeamA, m.TeamB)),
			slog.String("status", m.Status),
			slog.Int64("begin_at", m.Time.Unix()),
		)
		_, err = tx.Exec(
			`INSERT INTO matches (id, team_a, team_b, team_a_id, team_b_id, begin_at, status) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.TeamA, m.TeamB, m.TeamAID, m.TeamBID, m.Time.Unix(), m.Status,
		)
		if err != nil {
			return false, false, false, false, time.Time{}, "", "", "", err
		}
		if err := tx.Commit(); err != nil {
			return false, false, false, false, time.Time{}, "", "", "", err
		}
		return true, false, false, false, time.Time{}, "", "", "", nil
	} else if err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}

	timeChanged = dbTimeUnix != m.Time.Unix()
	teamsChanged = (dbTeamA != m.TeamA) || (dbTeamB != m.TeamB)
	statusChanged = dbStatus != m.Status

	slog.Debug("ProcessMatch update",
		slog.Int("match_id", m.ID),
		slog.String("teams", fmt.Sprintf("%s vs %s", m.TeamA, m.TeamB)),
		slog.String("old_status", dbStatus),
		slog.String("new_status", m.Status),
		slog.Bool("time_changed", timeChanged),
		slog.Bool("teams_changed", teamsChanged),
		slog.Bool("status_changed", statusChanged),
	)

	_, err = tx.Exec(
		`UPDATE matches SET begin_at = ?, team_a = ?, team_b = ?, team_a_id = ?, team_b_id = ?, status = ? WHERE id = ?`,
		m.Time.Unix(), m.TeamA, m.TeamB, m.TeamAID, m.TeamBID, m.Status, m.ID,
	)
	if err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}

	if err := tx.Commit(); err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}
	return false, timeChanged, teamsChanged, statusChanged, time.Unix(dbTimeUnix, 0), dbTeamA, dbTeamB, dbStatus, nil
}

func (s *Storage) GetUpcomingUserMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, '')
		FROM matches m
		INNER JOIN subscriptions s ON s.team_id IN (m.team_a_id, m.team_b_id)
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
		var teamAID, teamBID sql.NullInt64
		var status string
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Status = status
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *Storage) GetLiveUserMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, '')
		FROM matches m
		INNER JOIN subscriptions s ON s.team_id IN (m.team_a_id, m.team_b_id)
		WHERE s.user_id = ? AND m.begin_at <= ? AND m.status = 'running'
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
		var teamAID, teamBID sql.NullInt64
		var status string
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Status = status
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

func (s *Storage) CleanStaleRunningMatches(apiMatchIDs map[int]bool) {
	args := make([]any, 0, len(apiMatchIDs))
	placeholders := make([]string, 0, len(apiMatchIDs))
	for id := range apiMatchIDs {
		args = append(args, id)
		placeholders = append(placeholders, "?")
		slog.Debug("CleanStaleRunningMatches", slog.Int("api_match_id", id))
	}

	var query string
	if len(placeholders) > 0 {
		query = `UPDATE matches SET status = 'post_match'
			WHERE status = 'running' AND id NOT IN (` + strings.Join(placeholders, ",") + `)`
		_, err := s.db.Exec(query, args...)
		if err != nil {
			slog.Error("Ошибка при очистке зависших running-матчей", slog.Any("error", err))
		}
	} else {
		_, err := s.db.Exec(`UPDATE matches SET status = 'post_match' WHERE status = 'running'`)
		if err != nil {
			slog.Error("Ошибка при очистке зависших running-матчей", slog.Any("error", err))
		}
	}
}

func (s *Storage) Subscribe(userID, teamID int64, teamName string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO teams (id, name) VALUES (?, ?)`, teamID, teamName); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO subscriptions (user_id, team_id) VALUES (?, ?)`, userID, teamID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) Unsubscribe(userID, teamID int64) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE user_id = ? AND team_id = ?`, userID, teamID)
	return err
}

func (s *Storage) GetUsersByTeamIDs(teamAID, teamBID int) ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT user_id
		FROM subscriptions
		WHERE team_id IN (?, ?)
	`, teamAID, teamBID)
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

func (s *Storage) GetUserSubscriptions(userID int64) ([]domain.TeamInfo, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name
		FROM subscriptions s
		INNER JOIN teams t ON t.id = s.team_id
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
			continue
		}
		teams = append(teams, domain.TeamInfo{
			ID:   id,
			Name: name,
		})
	}
	return teams, rows.Err()
}

func (s *Storage) GetSubscribedTeamIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT team_id FROM subscriptions`)
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

	query := `SELECT id, team_a, team_b, begin_at, team_a_id, team_b_id
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
		var teamAID, teamBID sql.NullInt64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
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

func (s *Storage) GetTeamsByIDs(ids []int) ([]domain.TeamInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	args := make([]any, len(ids))
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		placeholders[i] = "?"
	}

	query := fmt.Sprintf("SELECT id, name FROM teams WHERE id IN (%s)", strings.Join(placeholders, ","))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []domain.TeamInfo
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		teams = append(teams, domain.TeamInfo{
			ID:   id,
			Name: name,
		})
	}
	return teams, rows.Err()
}

func (s *Storage) Close() error {
	return s.db.Close()
}
