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
