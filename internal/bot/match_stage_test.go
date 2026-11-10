package bot

import (
	"strings"
	"testing"

	"cs2bot/internal/domain"
)

func TestMatchStageRendering(t *testing.T) {
	for _, tt := range []struct {
		stage, round string
		playoff      bool
		bo           int
		want         string
	}{
		{"Group Stage", "Round 1", false, 1, " (BO1 · Группа)"},
		{"Group A", "Upper bracket final", false, 3, " (BO3 · Группа)"},
		{"Swiss Stage", "Round 5", false, 3, " (BO3 · Швейц.)"},
		{"Play-in", "Grand final", false, 3, " (BO3 · Плей-ин)"},
		{"Closed Qualifier", "Grand final", false, 3, " (BO3 · Квал.)"},
		{"Playoffs", "Round of 16", true, 3, " (BO3 · 1/8)"},
		{"Playoffs", "Quarter-final 2", true, 3, " (BO3 · 1/4)"},
		{"Playoffs", "Semifinals", true, 3, " (BO3 · 1/2)"},
		{"Playoffs", "Grand final", true, 5, " (BO5 · Финал)"},
		{"Playoffs", "Upper bracket final", true, 3, " (BO3 · Финал верх.)"},
		{"Playoffs", "Lower bracket semi-final 1", true, 3, " (BO3 · 1/2 ниж.)"},
		{"Playoffs", "Third place match", true, 3, " (BO3 · За 3-е)"},
		{"Playoffs", "Round 1", true, 3, " (BO3 · Плей-офф)"},
		{"Semi-finals", "", true, 3, " (BO3 · 1/2)"},
		{"Stage 1", "Team Final vs Team B", false, 3, " (BO3)"},
		{"", "", false, 3, " (BO3)"},
		{"Group Stage", "", false, 0, " (Группа)"},
		{"", "", false, 0, ""},
	} {
		t.Run(tt.stage+"/"+tt.round+tt.want, func(t *testing.T) {
			m := domain.Match{
				TeamA: "A", TeamB: "B", StageName: tt.stage, RoundName: tt.round,
				IsPlayoff: tt.playoff, NumberOfGames: tt.bo,
			}
			want := "A vs B" + tt.want + "\n"
			card := formatMatchCard(m, "A", "B", false, nil)
			if !strings.HasPrefix(card, want) {
				t.Errorf("карточка: %q, want prefix %q", card, want)
			}
			schedule := formatMatchesMessage("", nil, []domain.Match{m}, nil, 3, 0)
			if !strings.Contains(schedule, " | "+want) {
				t.Errorf("расписание: %q, want match %q", schedule, want)
			}
		})
	}
}
