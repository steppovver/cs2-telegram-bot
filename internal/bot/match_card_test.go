package bot

import (
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func testMatch() domain.Match {
	return domain.Match{
		ID:            1711369,
		TeamA:         "Team Falcons",
		TeamB:         "TYLOO",
		TeamAID:       130564,
		TeamBID:       3248,
		Time:          time.Date(2026, 10, 3, 9, 1, 0, 0, time.UTC),
		Status:        "running",
		NumberOfGames: 3,
	}
}

func TestFormatMatchCardHltv(t *testing.T) {
	m := testMatch()
	m.HltvURL = "https://www.hltv.org/matches/2398717/falcons-vs-tyloo-esl-pro-league-season-24"
	card := formatMatchCard(m, 3, 0)
	if !strings.Contains(card, "📊 HLTV:") {
		t.Fatalf("карточка без строки HLTV:\n%s", card)
	}
	if !strings.Contains(card, m.HltvURL) {
		t.Fatalf("карточка без URL:\n%s", card)
	}

	m.HltvURL = ""
	card = formatMatchCard(m, 3, 0)
	if strings.Contains(card, "HLTV") {
		t.Fatalf("карточка без ссылки не должна упоминать HLTV:\n%s", card)
	}
}

func TestBuildScoreBlockHltv(t *testing.T) {
	m := testMatch()
	m.HltvURL = "https://www.hltv.org/matches/2398717/falcons-vs-tyloo-esl-pro-league-season-24"
	block := buildScoreBlock(m, nil, 3, true)
	if !strings.Contains(block, "📊 HLTV:") || !strings.Contains(block, m.HltvURL) {
		t.Fatalf("блок счета без строки HLTV:\n%s", block)
	}

	m.HltvURL = ""
	block = buildScoreBlock(m, nil, 3, true)
	if strings.Contains(block, "HLTV") {
		t.Fatalf("блок без ссылки не должен упоминать HLTV:\n%s", block)
	}
}
