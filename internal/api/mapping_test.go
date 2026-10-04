package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMapPandaTournament(t *testing.T) {
	pm := pandaMatch{}
	pm.League.Name = "ESL Pro League"
	pm.Serie.FullName = "Season 24 2026"
	if got := mapPandaTournament(pm); got != "ESL Pro League Season 24 2026" {
		t.Errorf("mapPandaTournament = %q", got)
	}

	pm.Serie.FullName = ""
	if got := mapPandaTournament(pm); got != "ESL Pro League" {
		t.Errorf("mapPandaTournament без серии = %q", got)
	}

	if got := mapPandaTournament(pandaMatch{}); got != "" {
		t.Errorf("mapPandaTournament пустой = %q", got)
	}
}

func TestMapPandaMatchTournament(t *testing.T) {
	pm := pandaMatch{
		ID:        1711369,
		BeginAt:   time.Date(2026, 10, 3, 9, 1, 0, 0, time.UTC),
		Status:    "running",
		Opponents: []pandaOpponent{{Opponent: pandaTeam{ID: 130564, Name: "Team Falcons"}}, {Opponent: pandaTeam{ID: 3248, Name: "TYLOO"}}},
	}
	pm.League.Name = "ESL Pro League"
	pm.Serie.FullName = "Season 24 2026"
	m, ok := mapPandaMatch(pm)
	if !ok {
		t.Fatal("mapPandaMatch отклонил валидный матч")
	}
	if m.Tournament != "ESL Pro League Season 24 2026" {
		t.Errorf("Tournament = %q", m.Tournament)
	}

	pm.Status = "canceled"
	if _, ok := mapPandaMatch(pm); ok {
		t.Error("canceled должен отсекаться")
	}
}

// TestMapPandaMatchFromFixture гоняет реальный ответ API из api-examples
// через маппинг: ловит дрейф формата и проверяет конечные поля.
func TestMapPandaMatchFromFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api-examples", "falcons-match.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var matches []pandaMatch
	if err := json.Unmarshal(raw, &matches); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("fixture: матчей %d, want 1", len(matches))
	}

	m, ok := mapPandaMatch(matches[0])
	if !ok {
		t.Fatal("mapPandaMatch отклонил фикстуру")
	}
	if m.ID != 1711369 {
		t.Errorf("ID = %d, want 1711369", m.ID)
	}
	if m.TeamA != "Team Falcons" || m.TeamB != "TYLOO" {
		t.Errorf("команды = %q vs %q", m.TeamA, m.TeamB)
	}
	if m.TeamAID != 130564 || m.TeamBID != 3248 {
		t.Errorf("ID команд = %d vs %d", m.TeamAID, m.TeamBID)
	}
	if m.Tournament != "ESL Pro League Season 24 2026" {
		t.Errorf("Tournament = %q", m.Tournament)
	}
	if m.NumberOfGames != 3 {
		t.Errorf("NumberOfGames = %d, want 3", m.NumberOfGames)
	}
	if len(m.Streams) == 0 {
		t.Error("стримы потерялись")
	}
	official := false
	for _, s := range m.Streams {
		if s.Official {
			official = true
		}
	}
	if !official {
		t.Error("нет official-стрима (ESLCS)")
	}
}
