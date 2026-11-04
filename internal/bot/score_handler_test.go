package bot

import (
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestBuildScoreBlockRunning(t *testing.T) {
	m := domain.Match{
		TeamA: "G2", TeamB: "NAVI", TeamAID: 1, TeamBID: 2,
		NumberOfGames: 3,
		Tournament:    "EPL",
		Status:        "running",
		Time:          time.Now().Add(-30 * time.Minute),
		Results:       []domain.MatchResult{{TeamID: 1, Score: 1}, {TeamID: 2, Score: 0}},
		Streams: []domain.MatchStream{
			{URL: "https://www.twitch.tv/ESLCS", Official: true},
		},
	}
	subs := []domain.TeamInfo{{ID: 1, Name: "G2", Favorite: true}}
	got := buildScoreBlock(m, subs, 3, false, true)
	for _, want := range []string{
		"<u><b>G2</b></u> vs NAVI (BO3)",
		"Турнир:",
		"Серия (BO3):",
		"Идет:",
		"Официальная трансляция:",
		"Найти трансляцию:",
		"HLTV:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "Сыгран:") {
		t.Errorf("running must not show Сыгран: %q", got)
	}
}

func TestBuildScoreBlockFinishedNoTournamentDup(t *testing.T) {
	m := domain.Match{
		TeamA: "G2", TeamB: "NAVI", TeamAID: 1, TeamBID: 2,
		Tournament: "EPL",
		Status:     "finished",
		Time:       time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC),
		EndAt:      time.Date(2026, 11, 3, 16, 30, 0, 0, time.UTC),
		Results:    []domain.MatchResult{{TeamID: 1, Score: 2}, {TeamID: 2, Score: 1}},
	}
	got := buildScoreBlock(m, nil, 3, true, false)
	if strings.Contains(got, "Турнир:") {
		t.Errorf("finished under group header must not duplicate tournament: %q", got)
	}
	if !strings.Contains(got, "Сыгран:") {
		t.Errorf("finished needs Сыгран: %q", got)
	}
	if !strings.Contains(got, "Длился:") {
		t.Errorf("finished needs Длился: %q", got)
	}
}

func TestFormatScoreMessageSections(t *testing.T) {
	now := time.Now()
	matches := []domain.Match{
		{
			ID: 1, TeamA: "G2", TeamB: "NAVI", TeamAID: 1, TeamBID: 2,
			Status: "running", Tournament: "EPL", TournamentID: 7,
			Time:    now.Add(-20 * time.Minute),
			Results: []domain.MatchResult{{TeamID: 1, Score: 0}, {TeamID: 2, Score: 0}},
		},
		{
			ID: 2, TeamA: "FaZe", TeamB: "Vitality", TeamAID: 3, TeamBID: 4,
			Status: "finished", Tournament: "EPL", TournamentID: 7,
			Time:    now.Add(-3 * time.Hour),
			EndAt:   now.Add(-2 * time.Hour),
			Results: []domain.MatchResult{{TeamID: 3, Score: 2}, {TeamID: 4, Score: 0}},
		},
	}
	got := formatScoreMessage(matches, nil, 3, false)
	if !strings.Contains(got, "🔴 <b>Сейчас идут:</b>") {
		t.Fatalf("no running section: %q", got)
	}
	if !strings.Contains(got, "✅ <b>Завершенные:</b>") {
		t.Fatalf("no finished section: %q", got)
	}
	// Идущий — турнир в карточке; завершённый — только в заголовке группы.
	runningPart := got[:strings.Index(got, "✅")]
	finishedPart := got[strings.Index(got, "✅"):]
	if !strings.Contains(runningPart, "Турнир:") {
		t.Errorf("running card needs Турнир: %q", runningPart)
	}
	if strings.Count(finishedPart, "Турнир:") != 0 {
		t.Errorf("finished cards must not repeat Турнир: %q", finishedPart)
	}
	if !strings.Contains(finishedPart, "🏆") && !strings.Contains(finishedPart, "EPL") {
		t.Errorf("finished group needs tournament header: %q", finishedPart)
	}
}
