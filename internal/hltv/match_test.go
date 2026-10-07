package hltv

import (
	"context"
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestValidateMatchURL(t *testing.T) {
	falcons := domain.Match{TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026"}

	tests := []struct {
		name  string
		m     domain.Match
		url   string
		want  string
		valid bool
	}{
		{
			name:  "точное совпадение",
			m:     falcons,
			url:   "https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
			want:  "https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
			valid: true,
		},
		{
			name:  "обратный порядок команд и хост без www",
			m:     falcons,
			url:   "https://hltv.org/matches/2380123/tyloo-vs-falcons-esl-pro-league-season-24/",
			want:  "https://www.hltv.org/matches/2380123/tyloo-vs-falcons-esl-pro-league-season-24",
			valid: true,
		},
		{
			name: "матч тех же команд на другом турнире",
			m:    falcons,
			url:  "https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025",
		},
		{
			name: "другая команда",
			m:    falcons,
			url:  "https://www.hltv.org/matches/2380123/falcons-vs-navi-esl-pro-league-season-24",
		},
		{
			name: "чужой хост",
			m:    falcons,
			url:  "https://example.com/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
		},
		{
			name: "не страница матча",
			m:    falcons,
			url:  "https://www.hltv.org/team/8297/falcons",
		},
		{
			name:  "без турнира требований к событию нет",
			m:     domain.Match{TeamA: "G2", TeamB: "Natus Vincere"},
			url:   "https://www.hltv.org/matches/2380001/g2-vs-natus-vincere-blast-premier-fall-2026",
			want:  "https://www.hltv.org/matches/2380001/g2-vs-natus-vincere-blast-premier-fall-2026",
			valid: true,
		},
		{
			name:  "название без разделителей и точек",
			m:     domain.Match{TeamA: "Virtus.pro", TeamB: "FaZe Clan"},
			url:   "https://www.hltv.org/matches/2380002/virtuspro-vs-faze-iem-katowice",
			want:  "https://www.hltv.org/matches/2380002/virtuspro-vs-faze-iem-katowice",
			valid: true,
		},
		{
			name: "MOUZ NXT не подходит под MOUZ",
			m:    domain.Match{TeamA: "MOUZ", TeamB: "G2"},
			url:  "https://www.hltv.org/matches/2380003/mouz-nxt-vs-g2-esl-challenger",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ValidateMatchURL(tt.url, tt.m)
			if ok != tt.valid || got != tt.want {
				t.Errorf("ValidateMatchURL(%q) = (%q, %v), want (%q, %v)", tt.url, got, ok, tt.want, tt.valid)
			}
		})
	}
}

// fakeSearch отдает заготовленные результаты и считает вызовы.
type fakeSearch struct {
	results []string
	err     error
	queries []string
}

func (f *fakeSearch) Search(_ context.Context, query string) ([]string, error) {
	f.queries = append(f.queries, query)
	return f.results, f.err
}

func TestResolveMatchSkipsWrongCandidates(t *testing.T) {
	m := domain.Match{ID: 1, TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026"}
	sp := &fakeSearch{results: []string{
		"https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025",
		"https://www.hltv.org/team/8297/falcons",
		"https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24",
	}}
	got, err := ResolveMatch(context.Background(), sp, m)
	if err != nil {
		t.Fatalf("ResolveMatch: %v", err)
	}
	if want := "https://www.hltv.org/matches/2380123/falcons-vs-tyloo-esl-pro-league-season-24"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(sp.queries) != 1 || !strings.HasPrefix(sp.queries[0], "site:hltv.org/matches Team Falcons vs TYLOO") {
		t.Errorf("queries = %v", sp.queries)
	}
}

func TestResolveMatchNotFound(t *testing.T) {
	m := domain.Match{ID: 1, TeamA: "Team Falcons", TeamB: "TYLOO", Tournament: "ESL Pro League Season 24 2026"}
	sp := &fakeSearch{results: []string{"https://www.hltv.org/matches/2371000/falcons-vs-tyloo-iem-cologne-2025"}}
	got, err := ResolveMatch(context.Background(), sp, m)
	if err != nil || got != "" {
		t.Errorf("got (%q, %v), want пусто без ошибки", got, err)
	}
}

func TestMatchURL(t *testing.T) {
	m := domain.Match{
		TeamA:      "Team Falcons",
		TeamB:      "TYLOO",
		Tournament: "ESL Pro League Season 24 2026",
		Time:       time.Date(2026, 10, 3, 9, 1, 0, 0, time.UTC),
	}
	want := "https://www.google.com/search?btnI=1&q=site%3Ahltv.org%2Fmatches+Team+Falcons+vs+TYLOO+ESL+Pro+League+Season+24+2026+Oct+3"
	if got := MatchURL(m); got != want {
		t.Errorf("MatchURL = %q, want %q", got, want)
	}
}

func TestMatchURLNoTournament(t *testing.T) {
	m := domain.Match{TeamA: "G2", TeamB: "Natus Vincere"}
	got := MatchURL(m)
	if strings.Contains(got, "  ") || !strings.HasSuffix(got, "G2+vs+Natus+Vincere") {
		t.Errorf("MatchURL без турнира и даты = %q", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("MatchURL содержит неэскейпленные пробелы: %q", got)
	}
}

func TestMatchURLPrefersCached(t *testing.T) {
	m := domain.Match{TeamA: "G2", TeamB: "NAVI", HLTVURL: "https://www.hltv.org/matches/1/g2-vs-navi"}
	if got := MatchURL(m); got != m.HLTVURL {
		t.Errorf("MatchURL = %q, want закешированный", got)
	}
	m.HLTVURL = ""
	if got := MatchURL(m); !strings.HasPrefix(got, "https://www.google.com/search?") {
		t.Errorf("без кеша ожидался Google fallback, got %q", got)
	}
}

func TestEventURL(t *testing.T) {
	got := EventURL("ESL Pro League Season 24 2026")
	if !strings.HasPrefix(got, "https://www.google.com/search?btnI=1&q=site%3Ahltv.org%2Fevents+ESL+Pro+League") {
		t.Errorf("EventURL = %q", got)
	}
}
