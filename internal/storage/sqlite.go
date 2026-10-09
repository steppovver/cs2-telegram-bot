package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

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
	end_at INTEGER DEFAULT 0,
	notified INTEGER DEFAULT 0,
	status TEXT,
	streams_json TEXT NOT NULL DEFAULT '[]',
	score_json TEXT NOT NULL DEFAULT '[]',
	tournament_id INTEGER NOT NULL DEFAULT 0,
	hltv_url TEXT NOT NULL DEFAULT '',
	hltv_checked_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS tournaments (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	begin_at INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_teams_name ON teams(name);
CREATE INDEX IF NOT EXISTS idx_players_team_id ON players(team_id);
CREATE INDEX IF NOT EXISTS idx_matches_begin_at ON matches(begin_at);
CREATE INDEX IF NOT EXISTS idx_subscriptions_team ON subscriptions(team_id);
CREATE TABLE IF NOT EXISTS user_digest (
	user_id INTEGER PRIMARY KEY,
	enabled INTEGER NOT NULL DEFAULT 0,
	hour INTEGER NOT NULL DEFAULT 10,
	last_sent_date TEXT NOT NULL DEFAULT '',
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_digest_due ON user_digest(enabled, hour, last_sent_date);
CREATE TABLE IF NOT EXISTS user_settings (
	user_id INTEGER PRIMARY KEY,
	utc_offset INTEGER NOT NULL DEFAULT 3,
	FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

type Storage struct {
	db *sql.DB
}

func Open(dbPath string) (*sql.DB, error) {
	// PRAGMA foreign_keys действует на уровне соединения, поэтому включаем его
	// через DSN: драйвер применит его к каждому новому соединению пула.
	// _txlock=immediate: db.Begin() стартует BEGIN IMMEDIATE, писатели
	// сериализуются на busy_timeout, а не падают с SQLITE_BUSY_SNAPSHOT
	// при апгрейде read->write внутри транзакции.
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_foreign_keys=1&_txlock=immediate", dbPath)
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

	return db, nil
}

func InitSchema(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("ошибка создания схемы БД: %w", err)
	}
	return migrateSchema(db)
}

// matchColumnMigrations — колонки matches, добавленные после первой версии
// схемы. CREATE TABLE IF NOT EXISTS не меняет уже существующую таблицу,
// поэтому в старых БД недостающие колонки добавляются через ALTER.
var matchColumnMigrations = []struct{ name, def string }{
	{"hltv_url", "TEXT NOT NULL DEFAULT ''"},
	{"hltv_checked_at", "INTEGER NOT NULL DEFAULT 0"},
}

// migrateSchema идемпотентно добавляет недостающие колонки в matches.
func migrateSchema(db *sql.DB) error {
	for _, c := range matchColumnMigrations {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('matches') WHERE name = ?`, c.name).Scan(&n); err != nil {
			return fmt.Errorf("проверка колонки matches.%s: %w", c.name, err)
		}
		if n > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE matches ADD COLUMN ` + c.name + ` ` + c.def); err != nil {
			return fmt.Errorf("добавление колонки matches.%s: %w", c.name, err)
		}
		slog.Info("Миграция БД: добавлена колонка", slog.String("column", "matches."+c.name))
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

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&count); err == nil {
		slog.Info("Подключение к БД успешно", slog.Int("всего_команд", count))
	} else {
		slog.Error("Ошибка чтения команд из БД", slog.Any("error", err))
	}

	return s, nil
}

func (s *Storage) Close() error {
	return s.db.Close()
}
