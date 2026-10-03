package hltv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTeamTokens(t *testing.T) {
	cases := map[string][]string{
		"Team Falcons":    {"falcons"},
		"TYLOO":           {"tyloo"},
		"BC.Game Esports": {"bc", "game"},
		"Natus Vincere":   {"natus", "vincere"},
		"ShindeN":         {"shinden"},
		"1WIN":            {"1win"},
		"PARIVISION":      {"parivision"},
		"TBD":             {"tbd"},
		"":                nil,
	}
	for in, want := range cases {
		got := teamTokens(in)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("teamTokens(%q) = %v, want %v", in, got, want)
		}
	}
}

const fixturePage = `
<div>Results for October 3rd 2026</div>
<a href="/matches/2398724/furia-vs-betboom-esl-pro-league-season-24">FURIA vs BETBOOM</a>
<a href="/matches/2398723/g2-vs-natus-vincere-esl-pro-league-season-24">G2 vs NAVI</a>
<a href="/matches/2398717/falcons-vs-tyloo-esl-pro-league-season-24">Falcons vs TYLOO</a>
<div>Results for October 2nd 2026</div>
<a href="/matches/2398000/falcons-vs-tyloo-esl-pro-league-season-23">Falcons vs TYLOO</a>
<a href="/matches">Matches</a>
`

func TestFindBestPicksNearestDate(t *testing.T) {
	cands := parseCandidates(fixturePage)
	if len(cands) != 4 {
		t.Fatalf("parseCandidates = %d, want 4 (навигация /matches отсекается)", len(cands))
	}
	beginAt := time.Date(2026, 10, 3, 9, 1, 0, 0, time.UTC)
	href, ok := findBest(cands, "Team Falcons", "TYLOO", beginAt)
	if !ok {
		t.Fatal("findBest не нашел матч")
	}
	if href != "/matches/2398717/falcons-vs-tyloo-esl-pro-league-season-24" {
		t.Fatalf("findBest = %s, want свежий матч от 3 октября", href)
	}
}

func TestFindBestRejectsFarDate(t *testing.T) {
	cands := parseCandidates(fixturePage)
	// Пара та же, но begin_at на месяц раньше — оба кандидата вне допуска.
	beginAt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, ok := findBest(cands, "Team Falcons", "TYLOO", beginAt); ok {
		t.Fatal("findBest должен отказать: даты вне допуска 48ч")
	}
}

func TestFindBestNoFalsePositive(t *testing.T) {
	cands := parseCandidates(fixturePage)
	beginAt := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	if _, ok := findBest(cands, "Spirit", "TYLOO", beginAt); ok {
		t.Fatal("findBest нашел несуществующую пару Spirit vs TYLOO")
	}
	if _, ok := findBest(cands, "TBD", "TYLOO", beginAt); ok {
		t.Fatal("findBest сматчил TBD")
	}
}

func TestFindBestUndated(t *testing.T) {
	page := `<a href="/matches/2400001/spirit-vs-parivision-iem-katowice-2026">m</a>`
	cands := parseCandidates(page)
	href, ok := findBest(cands, "Spirit", "PARIVISION", time.Now())
	if !ok || href != "/matches/2400001/spirit-vs-parivision-iem-katowice-2026" {
		t.Fatalf("findBest без дат = %q, %v", href, ok)
	}
}

func TestBackoff(t *testing.T) {
	if Backoff(1) != time.Hour {
		t.Errorf("Backoff(1) = %v, want 1h", Backoff(1))
	}
	if Backoff(2) != 2*time.Hour {
		t.Errorf("Backoff(2) = %v, want 2h", Backoff(2))
	}
	if Backoff(5) != 3*time.Hour {
		t.Errorf("Backoff(5) = %v, want 3h cap", Backoff(5))
	}
}

func TestResolveAgainstFixtureServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixturePage))
	}))
	defer srv.Close()

	oldMatches, oldResults := matchesPageURL, resultsPageURL
	matchesPageURL, resultsPageURL = srv.URL+"/m", srv.URL+"/r"
	defer func() { matchesPageURL, resultsPageURL = oldMatches, oldResults }()

	beginAt := time.Date(2026, 10, 3, 9, 1, 0, 0, time.UTC)
	url, err := Resolve(context.Background(), "Team Falcons", "TYLOO", beginAt, "finished")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(url, "/matches/2398717/falcons-vs-tyloo-esl-pro-league-season-24") {
		t.Fatalf("Resolve = %s", url)
	}

	if _, err := Resolve(context.Background(), "Spirit", "MOUZ", beginAt, "finished"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve несуществующей пары = %v, want ErrNotFound", err)
	}
}
