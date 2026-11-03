package storage

import (
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestStreamChannelsSharedAcrossMatches(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()

	if err := s.Subscribe(1, 10, "TeamA"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := s.Subscribe(1, 20, "TeamB"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if err := s.Subscribe(1, 30, "TeamC"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	yt := "https://www.youtube.com/watch?v=Im3OHRzxkeg"
	put := func(id, aID, bID int, a, b string) {
		_, err := s.ProcessMatch(domain.Match{
			ID: id, TeamA: a, TeamB: b, TeamAID: aID, TeamBID: bID,
			Time: now.Add(time.Hour), Status: "not_started",
			Streams: []domain.MatchStream{{URL: yt, Official: true}},
		})
		if err != nil {
			t.Fatalf("ProcessMatch(%d): %v", id, err)
		}
	}
	put(1, 10, 20, "TeamA", "TeamB")
	put(2, 10, 30, "TeamA", "TeamC")

	if err := s.SaveStreamChannel("Im3OHRzxkeg", "ESL CS", "https://www.youtube.com/@ESLCS", now); err != nil {
		t.Fatalf("SaveStreamChannel: %v", err)
	}

	live, upcoming, err := s.GetScheduleMatches(1, 0, false)
	if err != nil {
		t.Fatalf("GetScheduleMatches: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live=%d", len(live))
	}
	if len(upcoming) != 2 {
		t.Fatalf("upcoming=%d", len(upcoming))
	}
	for _, m := range upcoming {
		if len(m.Streams) != 1 || m.Streams[0].Channel != "ESL CS" {
			t.Errorf("match %d streams=%+v", m.ID, m.Streams)
		}
	}

	// Channel не должен попадать в streams_json.
	var raw string
	if err := s.db.QueryRow(`SELECT streams_json FROM matches WHERE id = 1`).Scan(&raw); err != nil {
		t.Fatalf("streams_json: %v", err)
	}
	if strings.Contains(raw, "ESL") || strings.Contains(raw, "channel") {
		t.Errorf("streams_json must not store Channel: %s", raw)
	}
}

func TestGetYouTubeVideosForResolve(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()

	_, err := s.ProcessMatch(domain.Match{
		ID: 1, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
		Time: now.Add(time.Hour), Status: "not_started",
		Streams: []domain.MatchStream{
			{URL: "https://www.youtube.com/watch?v=Im3OHRzxkeg"},
			{URL: "https://www.twitch.tv/navi"},
			{URL: "https://youtu.be/AbCdEfGhIjK"},
		},
	})
	if err != nil {
		t.Fatalf("ProcessMatch: %v", err)
	}

	todo, err := s.GetYouTubeVideosForResolve(now.Add(-time.Hour), now.Add(2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetYouTubeVideosForResolve: %v", err)
	}
	if len(todo) != 2 {
		t.Fatalf("todo=%v, want 2 ids", todo)
	}

	// Успешный резолв убирает id из todo.
	if err := s.SaveStreamChannel("Im3OHRzxkeg", "NAVI", "", now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	todo, err = s.GetYouTubeVideosForResolve(now.Add(-time.Hour), now.Add(2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetYouTubeVideosForResolve: %v", err)
	}
	if len(todo) != 1 || todo[0] != "AbCdEfGhIjK" {
		t.Fatalf("todo=%v", todo)
	}

	// Негативный кеш свежий — не повторяем.
	if err := s.SaveStreamChannel("AbCdEfGhIjK", "", "", now); err != nil {
		t.Fatalf("Save negative: %v", err)
	}
	todo, err = s.GetYouTubeVideosForResolve(now.Add(-time.Hour), now.Add(2*time.Hour), now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetYouTubeVideosForResolve: %v", err)
	}
	if len(todo) != 0 {
		t.Fatalf("fresh negative must skip, todo=%v", todo)
	}

	// Негативный кеш протух — снова в todo.
	todo, err = s.GetYouTubeVideosForResolve(now.Add(-time.Hour), now.Add(2*time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("GetYouTubeVideosForResolve: %v", err)
	}
	if len(todo) != 1 || todo[0] != "AbCdEfGhIjK" {
		t.Fatalf("stale negative todo=%v", todo)
	}
}

func TestCleanOldStreamChannels(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()
	if err := s.SaveStreamChannel("oldVid11111", "Old", "", now.Add(-40*24*time.Hour)); err != nil {
		t.Fatalf("Save old: %v", err)
	}
	if err := s.SaveStreamChannel("newVid22222", "New", "", now); err != nil {
		t.Fatalf("Save new: %v", err)
	}
	if err := s.CleanOldStreamChannels(now.Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	ch, err := s.getStreamChannels([]string{"oldVid11111", "newVid22222"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := ch["oldVid11111"]; ok {
		t.Error("old row must be deleted")
	}
	if ch["newVid22222"] != "New" {
		t.Errorf("new row = %v", ch)
	}
}

func TestGetMatchesForReminderFillsChannel(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()
	_, err := s.ProcessMatch(domain.Match{
		ID: 7, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
		Time: now.Add(3 * time.Minute), Status: "not_started",
		Streams: []domain.MatchStream{{URL: "https://www.youtube.com/watch?v=Im3OHRzxkeg"}},
	})
	if err != nil {
		t.Fatalf("ProcessMatch: %v", err)
	}
	if err := s.SaveStreamChannel("Im3OHRzxkeg", "BLAST", "", now); err != nil {
		t.Fatalf("Save: %v", err)
	}
	ms, err := s.GetMatchesForReminder()
	if err != nil {
		t.Fatalf("GetMatchesForReminder: %v", err)
	}
	found := false
	for _, m := range ms {
		if m.ID != 7 {
			continue
		}
		found = true
		if len(m.Streams) != 1 || m.Streams[0].Channel != "BLAST" {
			t.Errorf("streams=%+v", m.Streams)
		}
	}
	if !found {
		t.Fatal("match 7 not in reminder set")
	}
}
