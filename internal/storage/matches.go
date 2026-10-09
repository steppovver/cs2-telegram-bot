package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/domain"
)

// matchSelectCols — общий набор колонок для выборок domain.Match.
// JOIN tournaments t обязателен у вызывающего запроса.
const matchSelectCols = `m.id, m.team_a, m.team_b, m.begin_at, COALESCE(m.end_at, 0), m.team_a_id, m.team_b_id, COALESCE(m.status, ''), COALESCE(m.streams_json, '[]'), COALESCE(m.score_json, '[]'), m.tournament_id, COALESCE(t.name, ''), COALESCE(t.begin_at, 0), m.hltv_url`

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

// scanMatchRow читает строку с колонками matchSelectCols в domain.Match.
func scanMatchRow(rows *sql.Rows) (domain.Match, error) {
	var m domain.Match
	var unixTime, endUnix int64
	var teamAID, teamBID sql.NullInt64
	var status string
	var streamsRaw, scoreRaw, tournamentName sql.NullString
	var tournamentBegin int64
	if err := rows.Scan(
		&m.ID, &m.TeamA, &m.TeamB, &unixTime, &endUnix,
		&teamAID, &teamBID, &status, &streamsRaw, &scoreRaw,
		&m.TournamentID, &tournamentName, &tournamentBegin, &m.HLTVURL,
	); err != nil {
		return domain.Match{}, err
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
	return m, nil
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

// GetScheduleMatches возвращает live и предстоящие матчи пользователя одним запросом.
// untilUnix — правая граница для upcoming (begin_at <= untilUnix); 0 = без границы.
// Live (status=running) в окно не режется.
func (s *Storage) GetScheduleMatches(userID int64, untilUnix int64) (live, upcoming []domain.Match, err error) {
	nowUnix := time.Now().Unix()
	rows, err := s.db.Query(`
		SELECT DISTINCT `+matchSelectCols+`
		FROM matches m
		LEFT JOIN tournaments t ON t.id = m.tournament_id
		INNER JOIN subscriptions s ON s.team_id IN (m.team_a_id, m.team_b_id)
		WHERE s.user_id = ? AND (
			(m.begin_at <= ? AND m.status = 'running')
			OR (m.begin_at > ? AND (? = 0 OR m.begin_at <= ?))
		)
		ORDER BY m.begin_at ASC
	`, userID, nowUnix, nowUnix, untilUnix, untilUnix)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	for rows.Next() {
		m, err := scanMatchRow(rows)
		if err != nil {
			continue
		}
		if m.Status == "running" && m.Time.Unix() <= nowUnix {
			live = append(live, m)
		} else {
			upcoming = append(upcoming, m)
		}
	}
	return live, upcoming, rows.Err()
}

// GetScoreMatches возвращает матчи со счетом для кнопки 📊: идущие сейчас
// и завершенные (finished + локальный post_match). Только из БД, без API.
// not_started сюда не попадают — счета у них нет.
func (s *Storage) GetScoreMatches(userID int64) ([]domain.Match, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT `+matchSelectCols+`
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
		m, err := scanMatchRow(rows)
		if err != nil {
			continue
		}
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

func (s *Storage) GetMatchesForReminder() ([]domain.Match, error) {
	now := time.Now().Unix()
	fiveMinsLater := now + (5 * 60)

	rows, err := s.db.Query(`
		SELECT `+matchSelectCols+`
		FROM matches m
		LEFT JOIN tournaments t ON t.id = m.tournament_id
		WHERE m.begin_at > ? AND m.begin_at <= ? AND m.notified = 0
	`, now, fiveMinsLater)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var matches []domain.Match
	for rows.Next() {
		m, err := scanMatchRow(rows)
		if err != nil {
			continue
		}
		matches = append(matches, m)
	}
	return matches, rows.Err()
}

func (s *Storage) MarkMatchAsNotified(matchID int) error {
	_, err := s.db.Exec(`UPDATE matches SET notified = 1 WHERE id = ?`, matchID)
	return err
}
