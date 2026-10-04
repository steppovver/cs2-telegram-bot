package bot

import (
	"strings"
	"testing"

	"cs2bot/internal/domain"
)

func TestHltvMatchURL(t *testing.T) {
	m := domain.Match{
		TeamA:      "Team Falcons",
		TeamB:      "TYLOO",
		Tournament: "ESL Pro League Season 24 2026",
	}
	want := "https://duckduckgo.com/?q=%21ducky+site%3Ahltv.org%2Fmatches+Team+Falcons+vs+TYLOO+ESL+Pro+League+Season+24+2026"
	if got := hltvMatchURL(m); got != want {
		t.Errorf("hltvMatchURL = %q, want %q", got, want)
	}
}

func TestHltvMatchURLNoTournament(t *testing.T) {
	m := domain.Match{TeamA: "G2", TeamB: "Natus Vincere"}
	got := hltvMatchURL(m)
	if strings.Contains(got, "  ") || !strings.HasSuffix(got, "G2+vs+Natus+Vincere") {
		t.Errorf("hltvMatchURL без турнира = %q", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("hltvMatchURL содержит неэскейпленные пробелы: %q", got)
	}
}

func TestHltvMatchLine(t *testing.T) {
	m := domain.Match{TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League"}
	line := hltvMatchLine(m)
	if !strings.HasPrefix(line, "📊 HLTV: <a href=\"https://duckduckgo.com/?q=") {
		t.Errorf("hltvMatchLine = %q", line)
	}
	if !strings.HasSuffix(line, "\">Профиль матча</a>") {
		t.Errorf("hltvMatchLine = %q", line)
	}

	for _, m := range []domain.Match{
		{TeamA: "TBD", TeamB: "TYLOO"},
		{TeamA: "G2", TeamB: "TBD"},
	} {
		if got := hltvMatchLine(m); got != "" {
			t.Errorf("hltvMatchLine(TBD) = %q, want пусто", got)
		}
	}
}
