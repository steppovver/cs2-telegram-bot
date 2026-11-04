package storage

import (
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestRetireCanceledAndZeroTime(t *testing.T) {
	s := openTestStorage(t)
	begin := time.Now().Add(2 * time.Minute).UTC().Truncate(time.Second)
	if _, err := s.ProcessMatch(domain.Match{
		ID: 1, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
		Time: begin, Status: domain.StatusNotStarted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Subscribe(1, 1, "A", false); err != nil {
		t.Fatal(err)
	}

	change, err := s.ProcessMatch(domain.Match{
		ID: 1, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
		Time: begin, Status: domain.StatusCanceled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !change.StatusChanged || change.IsNew || change.OldStatus != domain.StatusNotStarted {
		t.Fatalf("cancel change = %+v", change)
	}

	_, upcoming, err := s.GetScheduleMatches(1, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(upcoming) != 0 {
		t.Fatalf("canceled match still on schedule: %+v", upcoming)
	}
	reminders, err := s.GetMatchesForReminder()
	if err != nil {
		t.Fatal(err)
	}
	if len(reminders) != 0 {
		t.Fatalf("canceled match still reminded: %+v", reminders)
	}

	change, err = s.ProcessMatch(domain.Match{
		ID: 2, Status: domain.StatusCanceled, Time: begin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if change.Any() {
		t.Fatalf("new canceled must not insert: %+v", change)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM matches WHERE id = 2`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("new canceled row was inserted")
	}

	if _, err := s.ProcessMatch(domain.Match{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM matches WHERE id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("zero begin_at must delete the stored match")
	}
	if _, err := s.ProcessMatch(domain.Match{ID: 9}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM matches WHERE id = 9`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("new match without time must not be inserted")
	}
}
