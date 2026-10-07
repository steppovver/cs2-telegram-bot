package bot

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
	"cs2bot/internal/storage"
)

func TestAttachHLTV(t *testing.T) {
	st, err := storage.NewStorage(filepath.Join(t.TempDir(), "attach.db"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	now := time.Now()
	for _, m := range []domain.Match{
		{ID: 1, TeamA: "G2", TeamB: "NAVI", TeamAID: 1, TeamBID: 2, Time: now.Add(time.Hour), Status: "not_started"},
		{ID: 2, TeamA: "A", TeamB: "B", TeamAID: 3, TeamBID: 4, Time: now.Add(2 * time.Hour), Status: "not_started"},
	} {
		if _, _, _, _, _, _, _, _, err := st.ProcessMatch(m); err != nil {
			t.Fatalf("ProcessMatch(%d): %v", m.ID, err)
		}
	}
	if err := st.SetMatchHLTV(1, "https://www.hltv.org/matches/1/a-vs-b", now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMatchHLTV(2, "", now); err != nil {
		t.Fatal(err)
	}

	b := &Bot{storage: st}
	ms := []domain.Match{{ID: 1}, {ID: 2}, {ID: 3}}
	b.attachHLTV(ms)

	if ms[0].HLTVURL != "https://www.hltv.org/matches/1/a-vs-b" {
		t.Errorf("match 1 HLTVURL = %q", ms[0].HLTVURL)
	}
	if ms[1].HLTVURL != "" || ms[2].HLTVURL != "" {
		t.Errorf("не найденные матчи не должны получать ссылку: %q %q", ms[1].HLTVURL, ms[2].HLTVURL)
	}
}

func TestHltvMatchLine(t *testing.T) {
	m := domain.Match{TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League"}
	line := hltvMatchLine(m)
	if !strings.HasPrefix(line, "📊 HLTV: <a href=\"https://www.google.com/search?") {
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

	cached := domain.Match{
		TeamA: "G2", TeamB: "NAVI",
		HLTVURL: "https://www.hltv.org/matches/1/g2-vs-navi",
		Time:    time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
	}
	if got := hltvMatchLine(cached); !strings.Contains(got, cached.HLTVURL) {
		t.Errorf("hltvMatchLine с кешем = %q", got)
	}
}
