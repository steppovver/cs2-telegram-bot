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

func TestHltvLinkFlow(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now().Unix()

	mk := func(id int, teamA, teamB string, beginAt int64) {
		if _, _, _, _, _, _, _, _, err := s.ProcessMatch(domain.Match{
			ID: id, TeamA: teamA, TeamB: teamB,
			TeamAID: id*10 + 1, TeamBID: id*10 + 2,
			Time: time.Unix(beginAt, 0), Status: "not_started",
		}); err != nil {
			t.Fatalf("ProcessMatch(%d): %v", id, err)
		}
		if err := s.EnsureHltvLinkRow(id); err != nil {
			t.Fatalf("EnsureHltvLinkRow(%d): %v", id, err)
		}
	}
	// 1: начался час назад, 2: через 11 часов, 3: далеко за окном.
	mk(1, "Team Falcons", "TYLOO", now-3600)
	mk(2, "G2", "Natus Vincere", now+11*3600)
	mk(3, "Spirit", "PARIVISION", now+30*3600)

	due, err := s.GetHltvDueIDs(now, now-12*3600, now+12*3600, 100)
	if err != nil {
		t.Fatalf("GetHltvDueIDs: %v", err)
	}
	if len(due) != 2 || due[0] != 1 || due[1] != 2 {
		t.Fatalf("GetHltvDueIDs = %v, want [1 2] (приоритет: ближайшие к now)", due)
	}

	// Клейм до скрапинга: attempts+1 и перенос next_try.
	if _, err := s.ClaimHltvAttempt(1, now+3600); err != nil {
		t.Fatalf("ClaimHltvAttempt: %v", err)
	}
	if n, _ := s.HltvAttempts(1); n != 1 {
		t.Fatalf("HltvAttempts = %d, want 1", n)
	}
	due, _ = s.GetHltvDueIDs(now, now-12*3600, now+12*3600, 100)
	if len(due) != 1 || due[0] != 2 {
		t.Fatalf("после клейма due = %v, want [2]", due)
	}

	const url = "https://www.hltv.org/matches/2398717/falcons-vs-tyloo-esl-pro-league-season-24"
	if err := s.SetHltvURL(1, url); err != nil {
		t.Fatalf("SetHltvURL: %v", err)
	}
	got, err := s.HltvURLByIDs([]int{1, 2, 0, 1})
	if err != nil {
		t.Fatalf("HltvURLByIDs: %v", err)
	}
	if len(got) != 1 || got[1] != url {
		t.Fatalf("HltvURLByIDs = %v", got)
	}

	m, err := s.GetMatchByID(1)
	if err != nil {
		t.Fatalf("GetMatchByID: %v", err)
	}
	if m.TeamA != "Team Falcons" || m.TeamB != "TYLOO" {
		t.Fatalf("GetMatchByID = %+v", m)
	}
}
