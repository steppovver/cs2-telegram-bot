package storage

import (
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestKeepTournamentBeginAndStreams(t *testing.T) {
	s := openTestStorage(t)
	begin := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	match := domain.Match{
		ID: 1, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
		Time: time.Now().Add(time.Hour), Status: domain.StatusNotStarted,
		TournamentID: 9, Tournament: "EPL", TournamentBeginAt: begin,
		Streams: []domain.MatchStream{{URL: "https://twitch.tv/esl", Official: true}},
		Results: []domain.MatchResult{{TeamID: 1, Score: 1}},
	}
	if _, err := s.ProcessMatch(match); err != nil {
		t.Fatal(err)
	}
	match.Tournament = ""
	match.TournamentBeginAt = time.Time{}
	match.Streams = nil
	match.Results = nil
	if _, err := s.ProcessMatch(match); err != nil {
		t.Fatal(err)
	}
	var name string
	var tournamentBegin int64
	if err := s.db.QueryRow(`SELECT name, begin_at FROM tournaments WHERE id = 9`).Scan(&name, &tournamentBegin); err != nil {
		t.Fatal(err)
	}
	if name != "EPL" || tournamentBegin != begin.Unix() {
		t.Fatalf("tournament = %q %d", name, tournamentBegin)
	}
	var streams, score string
	if err := s.db.QueryRow(`SELECT streams_json, score_json FROM matches WHERE id = 1`).Scan(&streams, &score); err != nil {
		t.Fatal(err)
	}
	if streams == "[]" || streams == "" {
		t.Fatalf("streams wiped: %s", streams)
	}
	if score == "[]" || score == "" {
		t.Fatalf("score wiped: %s", score)
	}
}

func TestScheduleKeepsRecentNotStarted(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Subscribe(1, 1, "A", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessMatch(domain.Match{
		ID: 1, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
		Time: time.Now().Add(-2 * time.Hour), Status: domain.StatusNotStarted,
	}); err != nil {
		t.Fatal(err)
	}
	_, upcoming, err := s.GetScheduleMatches(1, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(upcoming) != 1 || upcoming[0].ID != 1 {
		t.Fatalf("upcoming = %+v, want the delayed match", upcoming)
	}
}

func TestSetMatchHLTVIgnoresChangedTeams(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()
	if _, err := s.ProcessMatch(domain.Match{
		ID: 1, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
		Time: now.Add(time.Hour), Status: domain.StatusNotStarted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMatchHLTV(1, "https://www.hltv.org/matches/1/a-vs-c", now, "A", "C"); err != nil {
		t.Fatal(err)
	}
	var url string
	if err := s.db.QueryRow(`SELECT hltv_url FROM matches WHERE id = 1`).Scan(&url); err != nil {
		t.Fatal(err)
	}
	if url != "" {
		t.Fatalf("url = %q, want empty", url)
	}
	if err := s.SetMatchHLTV(1, "https://www.hltv.org/matches/1/a-vs-b", now, "B", "A"); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT hltv_url FROM matches WHERE id = 1`).Scan(&url); err != nil {
		t.Fatal(err)
	}
	if url == "" {
		t.Fatal("swapped pair must still save")
	}
}
