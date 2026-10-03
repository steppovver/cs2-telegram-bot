package storage

import (
	"database/sql"
	"encoding/json"
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
	end_at INTEGER DEFAULT 0,
	notified INTEGER DEFAULT 0,
	status TEXT,
	streams_json TEXT NOT NULL DEFAULT '[]',
	score_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_teams_name ON teams(name);
CREATE INDEX IF NOT EXISTS idx_players_team_id ON players(team_id);
CREATE INDEX IF NOT EXISTS idx_matches_begin_at ON matches(begin_at);
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
CREATE TABLE IF NOT EXISTS hltv_links (
	match_id INTEGER PRIMARY KEY,
	hltv_url TEXT NOT NULL DEFAULT '',
	attempts INTEGER NOT NULL DEFAULT 0,
	next_try INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0,
	FOREIGN KEY (match_id) REFERENCES matches(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_hltv_next_try ON hltv_links(next_try);
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
		`ALTER TABLE matches ADD COLUMN streams_json TEXT DEFAULT '[]';`,
		`ALTER TABLE matches ADD COLUMN score_json TEXT DEFAULT '[]';`,
		`ALTER TABLE matches ADD COLUMN end_at INTEGER DEFAULT 0;`,
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

	if err := s.migrateDigestHoursToUTC(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ошибка миграции часов дайджеста: %w", err)
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

// migrateDigestHoursToUTC разово переводит user_digest.hour из локального
// wall-time в UTC: hour_utc = (hour_local - offset). Идемпотентно через meta.
// Старый прод хранил МСК-часы без строк в user_settings -> COALESCE дает 3.
func (s *Storage) migrateDigestHoursToUTC() error {
	var v string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'digest_hour_version'`).Scan(&v)
	if err == nil && v == "2" {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := s.db.Exec(`
		UPDATE user_digest SET hour = (
			(hour - COALESCE(
				(SELECT utc_offset FROM user_settings WHERE user_settings.user_id = user_digest.user_id),
				3) + 48) % 24)
	`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO meta (key, value) VALUES ('digest_hour_version', '2')
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		return err
	}
	slog.Info("Часы дайджеста переведены в UTC")
	return nil
}

// encodeStreams сериализует весь список стримов в JSON для колонки streams_json.
// Лимит max_streams_per_match применяется при рендере, а не в БД, чтобы смена
// конфига не требовала перефетча API.
func encodeStreams(streams []domain.MatchStream) string {
	if len(streams) == 0 {
		return "[]"
	}
	b, err := json.Marshal(streams)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeStreams разбирает streams_json обратно в слайс.
// Пустые/битые значения дают nil — рендер покажет fallback на поиск.
func decodeStreams(raw sql.NullString) []domain.MatchStream {
	s := ""
	if raw.Valid {
		s = strings.TrimSpace(raw.String)
	}
	if s == "" || s == "[]" {
		return nil
	}
	var out []domain.MatchStream
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	// Отсекаем мусор без URL на случай старых/битых записей.
	kept := out[:0]
	for _, st := range out {
		if strings.TrimSpace(st.URL) == "" {
			continue
		}
		kept = append(kept, st)
	}
	return kept
}

// scorePayload — формат колонки score_json: счет серии и карты одним JSON.
type scorePayload struct {
	Results  []domain.MatchResult `json:"results"`
	Games    []domain.MatchGame   `json:"games"`
	NumGames int                  `json:"num_games"`
}

// encodeScore сериализует счет серии и карты для колонки score_json.
func encodeScore(m domain.Match) string {
	if len(m.Results) == 0 && len(m.Games) == 0 && m.NumberOfGames == 0 {
		return "[]"
	}
	b, err := json.Marshal(scorePayload{Results: m.Results, Games: m.Games, NumGames: m.NumberOfGames})
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeScore разбирает score_json обратно в матч.
// Пустые/битые значения оставляют Results/Games/NumberOfGames пустыми.
func decodeScore(raw sql.NullString, m *domain.Match) {
	s := ""
	if raw.Valid {
		s = strings.TrimSpace(raw.String)
	}
	if s == "" || s == "[]" {
		return
	}
	var p scorePayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return
	}
	m.Results = p.Results
	m.Games = p.Games
	m.NumberOfGames = p.NumGames
}

// endAtUnix переводит EndAt в unix для БД: 0 = неизвестно.
func endAtUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *Storage) ProcessMatch(m domain.Match) (isNew bool, timeChanged bool, teamsChanged bool, statusChanged bool, oldTime time.Time, oldTeamA string, oldTeamB string, oldStatus string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}
	defer tx.Rollback()

	var dbTimeUnix int64
	var dbTeamA, dbTeamB, dbStatus string
	var dbNotified int

	err = tx.QueryRow(`SELECT begin_at, team_a, team_b, COALESCE(status, ''), COALESCE(notified, 0) FROM matches WHERE id = ?`, m.ID).
		Scan(&dbTimeUnix, &dbTeamA, &dbTeamB, &dbStatus, &dbNotified)

	if err == sql.ErrNoRows {
		slog.Debug("ProcessMatch new",
			slog.Int("match_id", m.ID),
			slog.String("teams", fmt.Sprintf("%s vs %s", m.TeamA, m.TeamB)),
			slog.String("status", m.Status),
			slog.Int64("begin_at", m.Time.Unix()),
		)
		_, err = tx.Exec(
			`INSERT INTO matches (id, team_a, team_b, team_a_id, team_b_id, begin_at, end_at, status, streams_json, score_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.TeamA, m.TeamB, m.TeamAID, m.TeamBID, m.Time.Unix(), endAtUnix(m.EndAt), m.Status, encodeStreams(m.Streams), encodeScore(m),
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
	teamsChanged = !sameTeamPair(dbTeamA, dbTeamB, m.TeamA, m.TeamB)
	statusChanged = dbStatus != m.Status

	slog.Debug("ProcessMatch update",
		slog.Int("match_id", m.ID),
		slog.String("teams", fmt.Sprintf("%s vs %s", m.TeamA, m.TeamB)),
		slog.Bool("time_changed", timeChanged),
		slog.Bool("teams_changed", teamsChanged),
		slog.Bool("status_changed", statusChanged),
	)

	// Сдвиг времени перевооружает напоминание "за 5 минут": иначе после
	// уже отправленного напоминания повторное не придет никогда.
	newNotified := dbNotified
	if timeChanged {
		newNotified = 0
	}

	_, err = tx.Exec(
		`UPDATE matches SET begin_at = ?, end_at = ?, team_a = ?, team_b = ?, team_a_id = ?, team_b_id = ?, status = ?, notified = ?, streams_json = ?, score_json = ? WHERE id = ?`,
		m.Time.Unix(), endAtUnix(m.EndAt), m.TeamA, m.TeamB, m.TeamAID, m.TeamBID, m.Status, newNotified, encodeStreams(m.Streams), encodeScore(m), m.ID,
	)
	if err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}

	if err := tx.Commit(); err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}
	return false, timeChanged, teamsChanged, statusChanged, time.Unix(dbTimeUnix, 0), dbTeamA, dbTeamB, dbStatus, nil
}

// sameTeamPair сравнивает пары команд без учета порядка: API не гарантирует
// порядок opponents, и простая перестановка местами — не смена соперника.
func sameTeamPair(a1, b1, a2, b2 string) bool {
	return (a1 == a2 && b1 == b2) || (a1 == b2 && b1 == a2)
}

func (s *Storage) GetUpcomingUserMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, ''), COALESCE(m.score_json, '[]')
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
		var scoreRaw sql.NullString
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status, &scoreRaw); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Status = status
		decodeScore(scoreRaw, &m)
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *Storage) GetLiveUserMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, ''), COALESCE(m.streams_json, '[]'), COALESCE(m.score_json, '[]')
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
		var streamsRaw, scoreRaw sql.NullString
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status, &streamsRaw, &scoreRaw); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Status = status
		m.Streams = decodeStreams(streamsRaw)
		decodeScore(scoreRaw, &m)
		matches = append(matches, m)
	}
	s.attachHltvURLs(matches)
	return matches, rows.Err()
}

// GetScoreMatches возвращает матчи со счетом для кнопки 📊: идущие сейчас
// и завершенные (finished + локальный post_match). Только из БД, без API.
// not_started сюда не попадают — счета у них нет.
func (s *Storage) GetScoreMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, COALESCE(m.end_at, 0), m.team_a_id, m.team_b_id, COALESCE(m.status, ''), COALESCE(m.streams_json, '[]'), COALESCE(m.score_json, '[]')
		FROM matches m
		INNER JOIN subscriptions s ON s.team_id IN (m.team_a_id, m.team_b_id)
		WHERE s.user_id = ? AND m.status IN ('running', 'finished', 'post_match')
		ORDER BY m.begin_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []domain.Match
	for rows.Next() {
		var m domain.Match
		var unixTime, endUnix int64
		var teamAID, teamBID sql.NullInt64
		var status string
		var streamsRaw, scoreRaw sql.NullString
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &endUnix, &teamAID, &teamBID, &status, &streamsRaw, &scoreRaw); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		if endUnix > 0 {
			m.EndAt = time.Unix(endUnix, 0)
		}
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Status = status
		m.Streams = decodeStreams(streamsRaw)
		decodeScore(scoreRaw, &m)
		matches = append(matches, m)
	}
	s.attachHltvURLs(matches)
	return matches, rows.Err()
}

func (s *Storage) CleanOldMatches() {
	res, err := s.db.Exec(`DELETE FROM matches WHERE begin_at < ?`, time.Now().Add(-24*time.Hour).Unix())
	if err != nil {
		slog.Error("Ошибка при очистке старых матчей", slog.Any("error", err))
		return
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		slog.Info("Очистка старых матчей", slog.Int64("deleted", n))
	}
}

func (s *Storage) CleanStaleRunningMatches(apiMatchIDs map[int]bool) {
	// Чанкуем IN-клаузу под лимит переменных SQLite (999/32766),
	// иначе при сотнях матчей очистка всегда падает с ошибкой.
	const chunkSize = 500
	ids := make([]any, 0, len(apiMatchIDs))
	for id := range apiMatchIDs {
		ids = append(ids, id)
	}
	slog.Debug("CleanStaleRunningMatches", slog.Int("api_matches", len(ids)))

	if len(ids) == 0 {
		_, err := s.db.Exec(`UPDATE matches SET status = 'post_match' WHERE status = 'running'`)
		if err != nil {
			slog.Error("Ошибка при очистке зависших running-матчей", slog.Any("error", err))
		}
		return
	}

	for start := 0; start < len(ids); start += chunkSize {
		end := start + chunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		placeholders := make([]string, len(chunk))
		for i := range chunk {
			placeholders[i] = "?"
		}
		query := `UPDATE matches SET status = 'post_match'
			WHERE status = 'running' AND id NOT IN (` + strings.Join(placeholders, ",") + `)`
		if _, err := s.db.Exec(query, chunk...); err != nil {
			slog.Error("Ошибка при очистке зависших running-матчей", slog.Any("error", err))
			return
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

// RemoveUser удаляет пользователя целиком. Подписки, настройки дайджеста
// и часовой пояс чистятся каскадом по FOREIGN KEY ... ON DELETE CASCADE.
// Вызывается рассылкой при мертвых получателях (бан бота, удаление чата).
func (s *Storage) RemoveUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, userID)
	return err
}

// GetStats возвращает счетчики для админ-панели тремя COUNT-запросами.
// Таблицы маленькие, отдельный запрос на каждую дешевле JOIN-агрегации.
func (s *Storage) GetStats() (domain.BotStats, error) {
	var st domain.BotStats
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&st.Users); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&st.Subscriptions); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM matches`).Scan(&st.Matches); err != nil {
		return st, err
	}
	return st, nil
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

	query := `SELECT id, team_a, team_b, begin_at, team_a_id, team_b_id, COALESCE(streams_json, '[]')
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
		var streamsRaw sql.NullString
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &streamsRaw); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Streams = decodeStreams(streamsRaw)
		matches = append(matches, m)
	}
	s.attachHltvURLs(matches)
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

const (
	// Дефолт 10:00 при UTC+3 == 07:00 UTC. Колонка hour хранит UTC.
	defaultDigestHourUTC = 7
	minDigestHour        = 0
	maxDigestHour        = 23
)

const (
	DefaultUTCOffset = 3
	MinUTCOffset     = -12
	MaxUTCOffset     = 14
)

func NormalizeUTCOffset(o int) int {
	if o < MinUTCOffset || o > MaxUTCOffset {
		return DefaultUTCOffset
	}
	return o
}

func (s *Storage) GetUserOffset(userID int64) (int, error) {
	var off int
	err := s.db.QueryRow(`SELECT utc_offset FROM user_settings WHERE user_id = ?`, userID).Scan(&off)
	if err == sql.ErrNoRows {
		return DefaultUTCOffset, nil
	}
	if err != nil {
		return DefaultUTCOffset, err
	}
	return NormalizeUTCOffset(off), nil
}

func (s *Storage) SetUserOffset(userID int64, offset int) error {
	if offset < MinUTCOffset || offset > MaxUTCOffset {
		return fmt.Errorf("сдвиг должен быть от %d до %+d", MinUTCOffset, MaxUTCOffset)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO user_settings (user_id, utc_offset)
		VALUES (?, ?)
		ON CONFLICT(user_id) DO UPDATE SET utc_offset = excluded.utc_offset`,
		userID, offset); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) GetUserOffsets(userIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(userIDs))
	for _, id := range userIDs {
		out[id] = DefaultUTCOffset
	}
	if len(userIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(userIDs))
	placeholders := make([]string, len(userIDs))
	for i, id := range userIDs {
		args[i] = id
		placeholders[i] = "?"
	}
	query := fmt.Sprintf("SELECT user_id, utc_offset FROM user_settings WHERE user_id IN (%s)",
		strings.Join(placeholders, ","))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var off int
		if err := rows.Scan(&id, &off); err != nil {
			continue
		}
		out[id] = NormalizeUTCOffset(off)
	}
	return out, rows.Err()
}

func (s *Storage) GetDigestSettings(userID int64) (domain.DigestSettings, error) {
	// HourUTC отдаем как есть из БД, конвертация в wall-time — задача bot-слоя.
	var enabled int
	var hourUTC int
	err := s.db.QueryRow(`SELECT enabled, hour FROM user_digest WHERE user_id = ?`, userID).Scan(&enabled, &hourUTC)
	if err == sql.ErrNoRows {
		off, _ := s.GetUserOffset(userID)
		return domain.DigestSettings{Enabled: false, HourUTC: defaultDigestHourUTC, UtcOffset: off}, nil
	}
	if err != nil {
		return domain.DigestSettings{}, err
	}
	off, _ := s.GetUserOffset(userID)
	return domain.DigestSettings{Enabled: enabled != 0, HourUTC: hourUTC, UtcOffset: off}, nil
}

func (s *Storage) SetDigestEnabled(userID int64, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO user_digest (user_id, enabled, hour)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET enabled = excluded.enabled`,
		userID, enabledInt, defaultDigestHourUTC); err != nil {
		return err
	}
	return tx.Commit()
}

// SetDigestHour пишет час в UTC (конвертация wall->UTC — задача bot-слоя).
func (s *Storage) SetDigestHour(userID int64, hourUTC int) error {
	if hourUTC < minDigestHour || hourUTC > maxDigestHour {
		return fmt.Errorf("час должен быть от %d до %d", minDigestHour, maxDigestHour)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO user_digest (user_id, enabled, hour)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET hour = excluded.hour`,
		userID, 0, hourUTC); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) GetDigestDueUsers(hourUTC int, slot string) ([]domain.DigestDueUser, error) {
	rows, err := s.db.Query(`SELECT d.user_id, d.hour, d.last_sent_date,
		COALESCE(st.utc_offset, ?) AS utc_offset
		FROM user_digest d
		LEFT JOIN user_settings st ON st.user_id = d.user_id
		WHERE d.enabled = 1 AND d.hour = ? AND d.last_sent_date != ?`,
		DefaultUTCOffset, hourUTC, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []domain.DigestDueUser
	for rows.Next() {
		var u domain.DigestDueUser
		var lastSent string
		if err := rows.Scan(&u.UserID, &u.HourUTC, &lastSent, &u.UtcOffset); err != nil {
			return nil, err
		}
		u.LastSent = lastSent
		u.UtcOffset = NormalizeUTCOffset(u.UtcOffset)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Storage) MarkDigestSent(userID int64, date string) error {
	_, err := s.db.Exec(`UPDATE user_digest SET last_sent_date = ? WHERE user_id = ?`, date, userID)
	return err
}

func (s *Storage) GetDigestMatches(userID int64, fromUnix, toUnix int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, '')
		FROM matches m
		INNER JOIN subscriptions s ON s.team_id IN (m.team_a_id, m.team_b_id)
		WHERE s.user_id = ? AND m.begin_at > ? AND m.begin_at <= ?
		ORDER BY m.begin_at ASC
	`, userID, fromUnix, toUnix)
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

func (s *Storage) Close() error {
	return s.db.Close()
}

// EnsureHltvLinkRow заводит строку-маркер для резолва ссылки HLTV.
// next_try=0 = к обработке прямо сейчас. Идемпотентно.
func (s *Storage) EnsureHltvLinkRow(matchID int) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO hltv_links (match_id, next_try) VALUES (?, 0)`, matchID)
	return err
}

// GetHltvDueIDs возвращает ID матчей без ссылки HLTV в окне begin_at,
// у которых next_try пуст или уже прошел. Сразу в порядке приоритета:
// ближайшие по времени начала к now — сверху.
func (s *Storage) GetHltvDueIDs(nowUnix, fromUnix, toUnix int64, limit int) ([]int, error) {
	rows, err := s.db.Query(`
		SELECT m.id FROM matches m
		LEFT JOIN hltv_links h ON h.match_id = m.id
		WHERE m.begin_at BETWEEN ? AND ?
			AND (h.hltv_url IS NULL OR h.hltv_url = '')
			AND COALESCE(h.next_try, 0) <= ?
		ORDER BY ABS(m.begin_at - ?) ASC
		LIMIT ?
	`, fromUnix, toUnix, nowUnix, nowUnix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// HltvAttempts возвращает число уже сделанных попыток скрапинга.
// Нет строки — 0 без ошибки.
func (s *Storage) HltvAttempts(matchID int) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT attempts FROM hltv_links WHERE match_id = ?`, matchID).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// ClaimHltvAttempt фиксирует взятие матча в работу: attempts+1 и время
// следующей попытки. Вызывается ДО скрапинга, поэтому упавшая попытка
// тоже откладывается, а не крутится по кругу.
func (s *Storage) ClaimHltvAttempt(matchID int, nextTryUnix int64) (int, error) {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO hltv_links (match_id, attempts, next_try, updated_at) VALUES (?, 1, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET attempts = hltv_links.attempts + 1, next_try = excluded.next_try, updated_at = excluded.updated_at`,
		matchID, nextTryUnix, now)
	if err != nil {
		return 0, err
	}
	return s.HltvAttempts(matchID)
}

// SetHltvURL сохраняет найденную ссылку. Матч с непустой ссылкой
// из выборки due выпадает навсегда.
func (s *Storage) SetHltvURL(matchID int, url string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO hltv_links (match_id, hltv_url, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(match_id) DO UPDATE SET hltv_url = excluded.hltv_url, updated_at = excluded.updated_at`,
		matchID, url, now)
	return err
}

// HltvURLByIDs батчем отдает найденные ссылки для подстановки в карточки.
func (s *Storage) HltvURLByIDs(ids []int) (map[int]string, error) {
	out := make(map[int]string)
	uniq := make([]int, 0, len(ids))
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}
	const chunkSize = 500
	for start := 0; start < len(uniq); start += chunkSize {
		end := start + chunkSize
		if end > len(uniq) {
			end = len(uniq)
		}
		chunk := uniq[start:end]
		args := make([]any, len(chunk))
		placeholders := make([]string, len(chunk))
		for i, id := range chunk {
			args[i] = id
			placeholders[i] = "?"
		}
		rows, err := s.db.Query(fmt.Sprintf(
			`SELECT match_id, hltv_url FROM hltv_links WHERE match_id IN (%s) AND hltv_url != ''`,
			strings.Join(placeholders, ",")),
			args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var id int
			var url string
			if err := rows.Scan(&id, &url); err != nil {
				continue
			}
			if url != "" {
				out[id] = url
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// GetMatchByID возвращает матч для HLTV-воркера: команды, время, статус.
func (s *Storage) GetMatchByID(matchID int) (domain.Match, error) {
	var m domain.Match
	var unixTime int64
	var teamAID, teamBID sql.NullInt64
	var status string
	err := s.db.QueryRow(`SELECT id, team_a, team_b, begin_at, team_a_id, team_b_id, COALESCE(status, '')
		FROM matches WHERE id = ?`, matchID).
		Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status)
	if err != nil {
		return m, err
	}
	m.Time = time.Unix(unixTime, 0)
	m.TeamAID = int(teamAID.Int64)
	m.TeamBID = int(teamBID.Int64)
	m.Status = status
	return m, nil
}

// attachHltvURLs добирает ссылки HLTV одним батч-запросом.
// Пустая карта — матчи остаются без ссылки, рендер ее просто не покажет.
func (s *Storage) attachHltvURLs(matches []domain.Match) {
	if len(matches) == 0 {
		return
	}
	ids := make([]int, 0, len(matches))
	for _, m := range matches {
		if m.ID != 0 {
			ids = append(ids, m.ID)
		}
	}
	urls, err := s.HltvURLByIDs(ids)
	if err != nil || len(urls) == 0 {
		return
	}
	for i := range matches {
		if u, ok := urls[matches[i].ID]; ok {
			matches[i].HltvURL = u
		}
	}
}
