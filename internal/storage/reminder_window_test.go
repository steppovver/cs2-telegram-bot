package storage

import (
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestReminderWindowAndMark(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now().UTC().Truncate(time.Second)
	put := func(id int, status string, begin time.Time) {
		t.Helper()
		if _, err := s.ProcessMatch(domain.Match{
			ID: id, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
			Time: begin, Status: status,
		}); err != nil {
			t.Fatalf("ProcessMatch %d: %v", id, err)
		}
	}
	put(1, domain.StatusNotStarted, now.Add(2*time.Minute))
	put(2, domain.StatusPostponed, now.Add(2*time.Minute))
	put(3, domain.StatusNotStarted, now.Add(-8*time.Minute))
	put(4, domain.StatusNotStarted, now.Add(-20*time.Minute))
	put(5, domain.StatusNotStarted, now.Add(8*time.Minute))

	got, err := s.GetMatchesForReminder()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int]bool{}
	for _, m := range got {
		ids[m.ID] = true
	}
	if !ids[1] || !ids[3] || ids[2] || ids[4] || ids[5] {
		t.Fatalf("reminder ids = %v, want 1 and 3", ids)
	}

	ok, err := s.MarkMatchAsNotified(1, now.Add(2*time.Minute))
	if err != nil || !ok {
		t.Fatalf("mark = %v, %v", ok, err)
	}
	ok, err = s.MarkMatchAsNotified(1, now.Add(2*time.Minute))
	if err != nil || ok {
		t.Fatalf("second mark = %v, %v, want false", ok, err)
	}

	shifted := now.Add(2 * time.Hour)
	if _, err := s.db.Exec(`UPDATE matches SET begin_at = ?, notified = 0 WHERE id = 3`, shifted.Unix()); err != nil {
		t.Fatal(err)
	}
	ok, err = s.MarkMatchAsNotified(3, now.Add(-8*time.Minute))
	if err != nil || ok {
		t.Fatalf("stale begin mark = %v, %v, want false", ok, err)
	}
	var notified int
	if err := s.db.QueryRow(`SELECT notified FROM matches WHERE id = 3`).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if notified != 0 {
		t.Fatalf("notified = %d after stale mark", notified)
	}
}
