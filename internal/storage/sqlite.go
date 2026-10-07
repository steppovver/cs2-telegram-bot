package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
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

// fillTournament переносит данные JOIN-строки tournaments в матч.
// Нет строки (id=0) — поля остаются пустыми, рендер положит матч
// в корзину "Прочие".
func fillTournament(m *domain.Match, name sql.NullString, beginUnix int64) {
	if name.Valid {
		m.Tournament = name.String
	}
	if beginUnix > 0 {
		m.TournamentBeginAt = time.Unix(beginUnix, 0)
	}
}

func (s *Storage) ProcessMatch(m domain.Match) (isNew bool, timeChanged bool, teamsChanged bool, statusChanged bool, oldTime time.Time, oldTeamA string, oldTeamB string, oldStatus string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, false, false, false, time.Time{}, "", "", "", err
	}
	defer tx.Rollback()

	// Турнир — отдельной строкой: имя/старт живут в tournaments,
	// в matches только ключ. Без serie (id=0) — пропускаем, матч
	// ляжет в корзину "Прочие".
	if m.TournamentID != 0 {
		if _, err := tx.Exec(`INSERT INTO tournaments (id, name, begin_at, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET name = excluded.name, begin_at = excluded.begin_at, updated_at = excluded.updated_at`,
			m.TournamentID, m.Tournament, endAtUnix(m.TournamentBeginAt), time.Now().Unix()); err != nil {
			return false, false, false, false, time.Time{}, "", "", "", err
		}
	}

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
			`INSERT INTO matches (id, team_a, team_b, team_a_id, team_b_id, begin_at, end_at, status, streams_json, score_json, tournament_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.TeamA, m.TeamB, m.TeamAID, m.TeamBID, m.Time.Unix(), endAtUnix(m.EndAt), m.Status, encodeStreams(m.Streams), encodeScore(m), m.TournamentID,
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

	// Смена соперника делает найденную страницу HLTV неверной: сбрасываем
	// ссылку, резолвер найдет ее заново для новой пары.
	resetHLTV := 0
	if teamsChanged {
		resetHLTV = 1
	}

	_, err = tx.Exec(
		`UPDATE matches SET begin_at = ?, end_at = ?, team_a = ?, team_b = ?, team_a_id = ?, team_b_id = ?, status = ?, notified = ?, streams_json = ?, score_json = ?, tournament_id = ?,
			hltv_url = CASE WHEN ? = 1 THEN '' ELSE hltv_url END,
			hltv_checked_at = CASE WHEN ? = 1 THEN 0 ELSE hltv_checked_at END
		WHERE id = ?`,
		m.Time.Unix(), endAtUnix(m.EndAt), m.TeamA, m.TeamB, m.TeamAID, m.TeamBID, m.Status, newNotified, encodeStreams(m.Streams), encodeScore(m), m.TournamentID,
		resetHLTV, resetHLTV, m.ID,
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
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, ''), COALESCE(m.score_json, '[]'), m.tournament_id, COALESCE(t.name, ''), COALESCE(t.begin_at, 0), m.hltv_url
		FROM matches m
		LEFT JOIN tournaments t ON t.id = m.tournament_id
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
		var scoreRaw, tournamentName sql.NullString
		var tournamentBegin int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status, &scoreRaw, &m.TournamentID, &tournamentName, &tournamentBegin, &m.HLTVURL); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Status = status
		decodeScore(scoreRaw, &m)
		fillTournament(&m, tournamentName, tournamentBegin)
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *Storage) GetLiveUserMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, ''), COALESCE(m.streams_json, '[]'), COALESCE(m.score_json, '[]'), m.tournament_id, COALESCE(t.name, ''), COALESCE(t.begin_at, 0), m.hltv_url
		FROM matches m
		LEFT JOIN tournaments t ON t.id = m.tournament_id
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
		var streamsRaw, scoreRaw, tournamentName sql.NullString
		var tournamentBegin int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status, &streamsRaw, &scoreRaw, &m.TournamentID, &tournamentName, &tournamentBegin, &m.HLTVURL); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Status = status
		m.Streams = decodeStreams(streamsRaw)
		decodeScore(scoreRaw, &m)
		fillTournament(&m, tournamentName, tournamentBegin)
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

// GetScoreMatches возвращает матчи со счетом для кнопки 📊: идущие сейчас
// и завершенные (finished + локальный post_match). Только из БД, без API.
// not_started сюда не попадают — счета у них нет.
func (s *Storage) GetScoreMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, COALESCE(m.end_at, 0), m.team_a_id, m.team_b_id, COALESCE(m.status, ''), COALESCE(m.streams_json, '[]'), COALESCE(m.score_json, '[]'), m.tournament_id, COALESCE(t.name, ''), COALESCE(t.begin_at, 0), m.hltv_url
		FROM matches m
		LEFT JOIN tournaments t ON t.id = m.tournament_id
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
		var streamsRaw, scoreRaw, tournamentName sql.NullString
		var tournamentBegin int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &endUnix, &teamAID, &teamBID, &status, &streamsRaw, &scoreRaw, &m.TournamentID, &tournamentName, &tournamentBegin, &m.HLTVURL); err != nil {
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
		fillTournament(&m, tournamentName, tournamentBegin)
		matches = append(matches, m)
	}
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

// SetMatchHLTV сохраняет результат поиска страницы матча на HLTV.
// Пустой url с checkedAt означает "искали, не нашли" (повтор по TTL).
func (s *Storage) SetMatchHLTV(matchID int, url string, checkedAt time.Time) error {
	if _, err := s.db.Exec(`UPDATE matches SET hltv_url = ?, hltv_checked_at = ? WHERE id = ?`,
		url, checkedAt.Unix(), matchID); err != nil {
		return fmt.Errorf("сохранение ссылки HLTV матча %d: %w", matchID, err)
	}
	return nil
}

// GetHLTVURLs возвращает найденные ссылки HLTV для набора матчей.
// Матчи без ссылки в результат не попадают.
func (s *Storage) GetHLTVURLs(matchIDs []int) (map[int]string, error) {
	out := make(map[int]string, len(matchIDs))
	if len(matchIDs) == 0 {
		return out, nil
	}
	const chunk = 500
	for start := 0; start < len(matchIDs); start += chunk {
		part := matchIDs[start:min(start+chunk, len(matchIDs))]

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(`SELECT id, hltv_url FROM matches WHERE hltv_url != '' AND id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("чтение ссылок HLTV: %w", err)
		}
		for rows.Next() {
			var id int
			var u string
			if err := rows.Scan(&id, &u); err != nil {
				rows.Close()
				return nil, fmt.Errorf("скан ссылки HLTV: %w", err)
			}
			out[id] = u
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("чтение ссылок HLTV: %w", err)
		}
	}
	return out, nil
}

// GetMatchesForHLTVResolve возвращает матчи с известными командами,
// начало которых попадает в [from, to], у которых ссылка HLTV еще не найдена
// и последняя попытка поиска была до retryBefore (или не было вовсе).
// Название турнира нужно для проверки найденной страницы.
func (s *Storage) GetMatchesForHLTVResolve(from, to, retryBefore time.Time) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT m.id, m.team_a, m.team_b, m.begin_at, COALESCE(t.name, '')
		FROM matches m
		LEFT JOIN tournaments t ON t.id = m.tournament_id
		WHERE m.begin_at >= ? AND m.begin_at <= ?
		  AND m.team_a != 'TBD' AND m.team_b != 'TBD'
		  AND m.hltv_url = '' AND m.hltv_checked_at < ?
		ORDER BY m.begin_at ASC
	`, from.Unix(), to.Unix(), retryBefore.Unix())
	if err != nil {
		return nil, fmt.Errorf("выборка матчей для HLTV: %w", err)
	}
	defer rows.Close()

	var matches []domain.Match
	for rows.Next() {
		var m domain.Match
		var unixTime int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &m.Tournament); err != nil {
			return nil, fmt.Errorf("скан матча для HLTV: %w", err)
		}
		m.Time = time.Unix(unixTime, 0)
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("выборка матчей для HLTV: %w", err)
	}
	return matches, nil
}

// staleRunningIDs возвращает id из running, которых нет в ответе API.
func staleRunningIDs(running []int, apiMatchIDs map[int]bool) []int {
	var stale []int
	for _, id := range running {
		if !apiMatchIDs[id] {
			stale = append(stale, id)
		}
	}
	return stale
}

// CleanStaleRunningMatches переводит в post_match матчи со статусом running,
// которых больше нет в ответе API. Разницу считаем в Go: NOT IN нельзя
// чанковать (каждый чанк пометил бы матчи из соседних чанков как зависшие),
// а UPDATE ... IN (stale) чанкуется безопасно. Все в одной транзакции.
func (s *Storage) CleanStaleRunningMatches(apiMatchIDs map[int]bool) {
	slog.Debug("CleanStaleRunningMatches", slog.Int("api_matches", len(apiMatchIDs)))

	n, err := s.markStaleRunning(apiMatchIDs)
	if err != nil {
		slog.Error("Ошибка при очистке зависших running-матчей", slog.Any("error", err))
		return
	}
	if n > 0 {
		slog.Info("Зависшие running-матчи переведены в post_match", slog.Int64("updated", n))
	}
}

func (s *Storage) markStaleRunning(apiMatchIDs map[int]bool) (int64, error) {
	// Чанкуем IN-клаузу под лимит переменных SQLite (999/32766).
	const chunkSize = 500

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("открытие транзакции: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT id FROM matches WHERE status = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("выборка running-матчей: %w", err)
	}
	var running []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("чтение id running-матча: %w", err)
		}
		running = append(running, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("обход running-матчей: %w", err)
	}
	// Закрываем до UPDATE: в транзакции одно соединение.
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("закрытие выборки running-матчей: %w", err)
	}

	stale := staleRunningIDs(running, apiMatchIDs)
	var updated int64
	for start := 0; start < len(stale); start += chunkSize {
		end := min(start+chunkSize, len(stale))
		chunk := stale[start:end]
		args := make([]any, len(chunk))
		placeholders := make([]string, len(chunk))
		for i, id := range chunk {
			args[i] = id
			placeholders[i] = "?"
		}
		res, err := tx.Exec(`UPDATE matches SET status = 'post_match'
			WHERE status = 'running' AND id IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return 0, fmt.Errorf("обновление зависших матчей: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			updated += n
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("коммит: %w", err)
	}
	return updated, nil
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

	query := `SELECT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.streams_json, '[]'), m.tournament_id, COALESCE(t.name, ''), COALESCE(t.begin_at, 0), m.hltv_url
	          FROM matches m
	          LEFT JOIN tournaments t ON t.id = m.tournament_id
	          WHERE m.begin_at > ? AND m.begin_at <= ? AND m.notified = 0`

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
		var streamsRaw, tournamentName sql.NullString
		var tournamentBegin int64
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &streamsRaw, &m.TournamentID, &tournamentName, &tournamentBegin, &m.HLTVURL); err != nil {
			continue
		}
		m.Time = time.Unix(unixTime, 0)
		m.TeamAID = int(teamAID.Int64)
		m.TeamBID = int(teamBID.Int64)
		m.Streams = decodeStreams(streamsRaw)
		fillTournament(&m, tournamentName, tournamentBegin)
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
		SELECT DISTINCT m.id, m.team_a, m.team_b, m.begin_at, m.team_a_id, m.team_b_id, COALESCE(m.status, ''), m.hltv_url
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
		if err := rows.Scan(&m.ID, &m.TeamA, &m.TeamB, &unixTime, &teamAID, &teamBID, &status, &m.HLTVURL); err != nil {
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
