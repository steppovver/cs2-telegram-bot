package storage

import (
	"path/filepath"
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
			Tournament: "ESL Pro League Season 24 2026",
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
