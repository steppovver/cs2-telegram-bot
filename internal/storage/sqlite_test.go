package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func openTestStorage(t *testing.T) *Storage {
	t.Helper()
	s, err := NewStorage(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestMatchTournamentColumn проверяет проброс колонки tournament:
// запись через ProcessMatch и чтение во всех селектах карточек.
func TestMatchTournamentColumn(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()

	if err := s.Subscribe(1, 130564, "Team Falcons"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := s.Subscribe(1, 3248, "TYLOO"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	put := func(id int, status string, beginAt time.Time) {
		_, _, _, _, _, _, _, _, err := s.ProcessMatch(domain.Match{
			ID: id, TeamA: "Team Falcons", TeamB: "TYLOO",
			TeamAID: 130564, TeamBID: 3248,
			Time: beginAt, Status: status,
			Tournament:        "ESL Pro League Season 24 2026",
			TournamentID:      11004,
			TournamentBeginAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("ProcessMatch(%d): %v", id, err)
		}
	}
	put(1, "finished", now.Add(-2*time.Hour))     // счёт
	put(2, "running", now.Add(-time.Hour))        // live
	put(3, "not_started", now.Add(2*time.Minute)) // напоминание

	ms, err := s.GetScoreMatches(1)
	if got := mustTournament(t, ms, err, 1); got != "ESL Pro League Season 24 2026" {
		t.Errorf("score Tournament = %q", got)
	}
	for _, m := range ms {
		if m.ID != 1 {
			continue
		}
		if m.TournamentID != 11004 {
			t.Errorf("score TournamentID = %d, want 11004", m.TournamentID)
		}
		wantBegin := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
		if !m.TournamentBeginAt.Equal(wantBegin) {
			t.Errorf("score TournamentBeginAt = %v, want %v", m.TournamentBeginAt, wantBegin)
		}
	}
	var tname string
	var tbegin int64
	if err := s.db.QueryRow(`SELECT name, begin_at FROM tournaments WHERE id = 11004`).Scan(&tname, &tbegin); err != nil {
		t.Fatalf("tournaments row: %v", err)
	}
	if tname != "ESL Pro League Season 24 2026" || tbegin != time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("tournaments row = %q %d", tname, tbegin)
	}
	ml, err := s.GetLiveUserMatches(1)
	if got := mustTournament(t, ml, err, 2); got != "ESL Pro League Season 24 2026" {
		t.Errorf("live Tournament = %q", got)
	}
	mr, err := s.GetMatchesForReminder()
	if got := mustTournament(t, mr, err, 3); got != "ESL Pro League Season 24 2026" {
		t.Errorf("reminder Tournament = %q", got)
	}
}

func mustTournament(t *testing.T, matches []domain.Match, err error, wantID int) string {
	t.Helper()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	for _, m := range matches {
		if m.ID == wantID {
			return m.Tournament
		}
	}
	t.Fatalf("матч %d не найден в выборке %+v", wantID, matches)
	return ""
}

// TestForeignKeysOnEveryConnection: foreign_keys задан в DSN, поэтому должен
// быть включен на всех соединениях пула, а не только на первом.
func TestForeignKeysOnEveryConnection(t *testing.T) {
	s := openTestStorage(t)
	ctx := context.Background()

	var conns []*sql.Conn
	for i := 0; i < 5; i++ {
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn %d: %v", i, err)
		}
		conns = append(conns, c)
		var fk int
		if err := c.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatalf("PRAGMA foreign_keys: %v", err)
		}
		if fk != 1 {
			t.Errorf("соединение %d: foreign_keys = %d, want 1", i, fk)
		}
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// TestRemoveUserCascade: удаление пользователя чистит подписки/настройки
// независимо от того, какое соединение пула выполнило DELETE.
func TestRemoveUserCascade(t *testing.T) {
	s := openTestStorage(t)

	for round := 0; round < 20; round++ {
		uid := int64(1000 + round)
		if err := s.Subscribe(uid, 1, "Team"); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		if err := s.SetUserOffset(uid, 5); err != nil {
			t.Fatalf("SetUserOffset: %v", err)
		}
		if err := s.SetDigestEnabled(uid, true); err != nil {
			t.Fatalf("SetDigestEnabled: %v", err)
		}
		if err := s.RemoveUser(uid); err != nil {
			t.Fatalf("RemoveUser: %v", err)
		}
		for _, table := range []string{"subscriptions", "user_settings", "user_digest"} {
			var n int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE user_id = ?`, uid).Scan(&n); err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if n != 0 {
				t.Fatalf("round %d: в %s осталось %d строк после RemoveUser", round, table, n)
			}
		}
	}
}

// TestCleanStaleRunningMatchesChunks: при числе матчей больше размера чанка
// в post_match уходят только матчи, которых нет в ответе API.
func TestCleanStaleRunningMatchesChunks(t *testing.T) {
	s := openTestStorage(t)
	const total = 1300
	begin := time.Now().Add(-time.Hour).Unix()

	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for id := 1; id <= total; id++ {
		if _, err := tx.Exec(`INSERT INTO matches (id, team_a, team_b, begin_at, status) VALUES (?, 'A', 'B', ?, 'running')`, id, begin); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Зависшие — из разных "чанков".
	stale := map[int]bool{3: true, 700: true, 1299: true}
	api := make(map[int]bool, total)
	for id := 1; id <= total; id++ {
		if !stale[id] {
			api[id] = true
		}
	}

	s.CleanStaleRunningMatches(api)

	rows, err := s.db.Query(`SELECT id, status FROM matches`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		want := "running"
		if stale[id] {
			want = "post_match"
		}
		if status != want {
			t.Errorf("матч %d: status = %q, want %q", id, status, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// Пустой ответ API — все running уходят в post_match.
	s.CleanStaleRunningMatches(map[int]bool{})
	var running int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM matches WHERE status = 'running'`).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 0 {
		t.Errorf("после пустого ответа API осталось running: %d", running)
	}
}

// TestConcurrentWrites: параллельные ProcessMatch/Subscribe не падают
// с SQLITE_BUSY (BEGIN IMMEDIATE + busy_timeout сериализуют писателей).
func TestConcurrentWrites(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now().Add(time.Hour)

	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				m := domain.Match{
					ID: 1 + i, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
					Time: now, Status: "not_started",
					Tournament: "T", TournamentID: 7, TournamentBeginAt: now,
				}
				if _, _, _, _, _, _, _, _, err := s.ProcessMatch(m); err != nil {
					errs <- fmt.Errorf("ProcessMatch worker %d: %w", w, err)
					return
				}
				if err := s.Subscribe(int64(w*100+i), 1, "A"); err != nil {
					errs <- fmt.Errorf("Subscribe worker %d: %w", w, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestHLTVColumns(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()

	put := func(id int, a, b string, beginAt time.Time) {
		_, _, _, _, _, _, _, _, err := s.ProcessMatch(domain.Match{
			ID: id, TeamA: a, TeamB: b, TeamAID: 1, TeamBID: 2, Time: beginAt, Status: "not_started",
			Tournament: "ESL Pro League", TournamentID: 7,
		})
		if err != nil {
			t.Fatalf("ProcessMatch(%d): %v", id, err)
		}
	}
	put(1, "G2", "NAVI", now.Add(2*time.Hour))
	put(2, "TBD", "NAVI", now.Add(2*time.Hour))
	put(3, "A", "B", now.Add(20*time.Hour))
	put(4, "C", "D", now.Add(3*time.Hour))

	retryBefore := now.Add(-time.Hour)
	ms, err := s.GetMatchesForHLTVResolve(now.Add(-time.Hour), now.Add(12*time.Hour), retryBefore)
	if err != nil {
		t.Fatalf("GetMatchesForHLTVResolve: %v", err)
	}
	if len(ms) != 2 || ms[0].ID != 1 || ms[1].ID != 4 || ms[0].Tournament != "ESL Pro League" {
		t.Fatalf("matches = %+v, want матчи 1 и 4 с турниром (TBD и вне окна отсечены)", ms)
	}

	// Найденная ссылка выпадает из выборки и читается через GetHLTVURLs.
	if err := s.SetMatchHLTV(1, "https://www.hltv.org/matches/1/g2-vs-navi", now); err != nil {
		t.Fatal(err)
	}
	// Свежая отметка "не нашли" тоже выпадает; старая — нет.
	if err := s.SetMatchHLTV(4, "", now); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.GetMatchesForHLTVResolve(now.Add(-time.Hour), now.Add(12*time.Hour), retryBefore)
	if len(ms) != 0 {
		t.Errorf("после сохранения ждали пустую выборку, got %+v", ms)
	}
	ms, _ = s.GetMatchesForHLTVResolve(now.Add(-time.Hour), now.Add(12*time.Hour), now.Add(time.Minute))
	if len(ms) != 1 || ms[0].ID != 4 {
		t.Errorf("по истечении TTL ждали матч 4, got %+v", ms)
	}
	urls, err := s.GetHLTVURLs([]int{1, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[1] != "https://www.hltv.org/matches/1/g2-vs-navi" {
		t.Errorf("GetHLTVURLs = %v", urls)
	}

	// Все селекты матчей отдают ссылку сразу в Match.HLTVURL.
	if err := s.Subscribe(1, 1, "G2"); err != nil {
		t.Fatal(err)
	}
	const want = "https://www.hltv.org/matches/1/g2-vs-navi"
	if ms, err := s.GetUpcomingUserMatches(1); err != nil || len(ms) == 0 || ms[0].ID != 1 || ms[0].HLTVURL != want {
		t.Errorf("GetUpcomingUserMatches: %+v, %v", ms, err)
	}
	fromUnix, toUnix := now.Unix(), now.Add(24*time.Hour).Unix()
	if ms, err := s.GetDigestMatches(1, fromUnix, toUnix); err != nil || len(ms) == 0 || ms[0].HLTVURL != want {
		t.Errorf("GetDigestMatches: %+v, %v", ms, err)
	}

	// Обновление матча без смены команд ссылку не трогает, смена соперника сбрасывает.
	put(1, "G2", "NAVI", now.Add(2*time.Hour))
	if urls, _ = s.GetHLTVURLs([]int{1}); urls[1] == "" {
		t.Error("ссылка потеряна при обновлении без смены команд")
	}
	put(1, "G2", "FaZe", now.Add(2*time.Hour))
	if urls, _ = s.GetHLTVURLs([]int{1}); urls[1] != "" {
		t.Error("ссылка должна сбрасываться при смене соперника")
	}
}

func TestGetHLTVURLsEmptyAndChunked(t *testing.T) {
	s := openTestStorage(t)

	for _, ids := range [][]int{nil, {}} {
		urls, err := s.GetHLTVURLs(ids)
		if err != nil || len(urls) != 0 {
			t.Errorf("GetHLTVURLs(%v) = (%v, %v), want пустую карту без ошибки", ids, urls, err)
		}
	}

	// Больше одного чанка (500 id) читается без ошибок.
	if _, _, _, _, _, _, _, _, err := s.ProcessMatch(domain.Match{
		ID: 700, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2, Time: time.Now().Add(time.Hour), Status: "not_started",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMatchHLTV(700, "https://www.hltv.org/matches/1/a-vs-b", time.Now()); err != nil {
		t.Fatal(err)
	}
	ids := make([]int, 1200)
	for i := range ids {
		ids[i] = i + 1
	}
	urls, err := s.GetHLTVURLs(ids)
	if err != nil || len(urls) != 1 || urls[700] == "" {
		t.Errorf("GetHLTVURLs(1200 ids) = (%v, %v)", urls, err)
	}
}

func TestMigrateSchemaAddsHLTVColumns(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// БД старой версии: matches без колонок HLTV и с данными.
	if _, err := db.Exec(`CREATE TABLE matches (id INTEGER PRIMARY KEY, team_a TEXT, team_b TEXT, begin_at INTEGER);
		INSERT INTO matches (id, team_a, team_b, begin_at) VALUES (1, 'A', 'B', 1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // повторный запуск не должен падать
		if err := InitSchema(db); err != nil {
			t.Fatalf("InitSchema #%d: %v", i+1, err)
		}
	}
	var url string
	var checked int64
	if err := db.QueryRow(`SELECT hltv_url, hltv_checked_at FROM matches WHERE id = 1`).Scan(&url, &checked); err != nil {
		t.Fatalf("колонки HLTV не добавлены: %v", err)
	}
	if url != "" || checked != 0 {
		t.Errorf("дефолты: url=%q checked=%d", url, checked)
	}
}
