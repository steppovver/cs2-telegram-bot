package bot

import (
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func tmatch(id, tid int, name string, tbegin time.Time, mtime time.Time) domain.Match {
	return domain.Match{
		ID: id, TeamA: "A", TeamB: "B",
		TournamentID: tid, Tournament: name, TournamentBeginAt: tbegin,
		Time: mtime,
	}
}

func TestGroupMatchesByTournament(t *testing.T) {
	epl := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	blast := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	matches := []domain.Match{
		tmatch(1, 11004, "ESL Pro League Season 24 2026", epl, epl.Add(2*time.Hour)),
		tmatch(2, 999, "BLAST Open Porto 2026", blast, blast.Add(2*time.Hour)),
		tmatch(3, 11004, "ESL Pro League Season 24 2026", epl, epl.Add(4*time.Hour)),
		tmatch(4, 0, "", time.Time{}, epl.Add(time.Hour)),
	}
	groups := groupMatchesByTournament(matches)
	if len(groups) != 3 {
		t.Fatalf("групп %d, want 3", len(groups))
	}
	// Порядок — по ближайшему матчу: BLAST, Прочие (epl+1h), EPL (epl+2h).
	if groups[0].Name != "BLAST Open Porto 2026" || len(groups[0].Matches) != 1 {
		t.Errorf("groups[0] = %+v", groups[0])
	}
	if groups[1].Name != "Прочие" || len(groups[1].Matches) != 1 {
		t.Errorf("groups[1] = %+v", groups[1])
	}
	if groups[2].Name != "ESL Pro League Season 24 2026" || len(groups[2].Matches) != 2 {
		t.Errorf("groups[2] = %+v", groups[2])
	}
	if groups[2].Matches[0].ID != 1 || groups[2].Matches[1].ID != 3 {
		t.Errorf("порядок внутри группы нарушен: %+v", groups[2].Matches)
	}
	if len(groupMatchesByTournament(nil)) != 0 {
		t.Error("пустой вход должен давать пусто")
	}
}

// Турнир с более поздним BeginAt, но более ранним матчем, идёт первым.
func TestGroupMatchesByTournamentSoonestMatch(t *testing.T) {
	eplBegin := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	romanBegin := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	stakeBegin := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	matches := []domain.Match{
		tmatch(1, 1, "ESL Pro League Season 24 2026", eplBegin, time.Date(2026, 10, 9, 13, 30, 0, 0, time.UTC)),
		tmatch(2, 2, "Roman Imperium Cup Season 9 2026", romanBegin, time.Date(2026, 10, 8, 13, 30, 0, 0, time.UTC)),
		tmatch(3, 3, "Stake Pulse Beat III 2026", stakeBegin, time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)),
		tmatch(4, 1, "ESL Pro League Season 24 2026", eplBegin, time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC)),
	}
	groups := groupMatchesByTournament(matches)
	if len(groups) != 3 {
		t.Fatalf("групп %d, want 3", len(groups))
	}
	want := []string{
		"Roman Imperium Cup Season 9 2026",
		"ESL Pro League Season 24 2026",
		"Stake Pulse Beat III 2026",
	}
	for i, name := range want {
		if groups[i].Name != name {
			t.Errorf("groups[%d] = %q, want %q", i, groups[i].Name, name)
		}
	}
}

func TestTournamentHeader(t *testing.T) {
	g := tournamentGroup{ID: 11004, Name: "ESL Pro League Season 24 2026"}
	h := tournamentHeader(g)
	if !strings.HasPrefix(h, "🏆 <a href=\"https://www.google.com/search?") {
		t.Errorf("header = %q", h)
	}
	if !strings.Contains(h, "ESL+Pro+League") {
		t.Errorf("header без турнира в ссылке: %q", h)
	}
	if got := tournamentHeader(tournamentGroup{Name: "Прочие"}); got != "🗂 Прочие" {
		t.Errorf("header Прочие = %q", got)
	}
}

func TestTournamentLine(t *testing.T) {
	m := domain.Match{Tournament: "ESL Pro League Season 24 2026"}
	line := tournamentLine(m)
	if !strings.HasPrefix(line, "🏟 Турнир: <a href=\"https://www.google.com/search?") {
		t.Errorf("line = %q", line)
	}
	if got := tournamentLine(domain.Match{}); got != "" {
		t.Errorf("line без турнира = %q", got)
	}
}

