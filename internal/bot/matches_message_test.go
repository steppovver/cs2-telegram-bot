package bot

import (
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestFormatMatchesMessage(t *testing.T) {
	subs := []domain.TeamInfo{{ID: 1, Name: "G2"}}
	live := []domain.Match{{
		ID: 10, TeamA: "G2", TeamB: "NAVI", TeamAID: 1, TeamBID: 2,
		Status: "running", Tournament: "EPL", TournamentID: 7,
	}}
	upcoming := []domain.Match{{
		ID: 11, TeamA: "G2", TeamB: "FaZe", TeamAID: 1, TeamBID: 3,
		Time: time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC),
		NumberOfGames: 3, Tournament: "EPL", TournamentID: 7,
		TournamentBeginAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC),
	}}

	body := formatMatchesMessage("", live, upcoming, subs, 3, 3)
	withHeader := formatMatchesMessage("⏰ <b>Матчи ваших команд на 24 часа:</b>", live, upcoming, subs, 3, 3)

	if !strings.HasPrefix(withHeader, "⏰ <b>Матчи ваших команд на 24 часа:</b>\n\n") {
		t.Fatalf("нет заголовка: %q", withHeader[:min(80, len(withHeader))])
	}
	if !strings.HasPrefix(body, "🔴 <b>Сейчас играют:</b>") {
		t.Fatalf("тело без live-блока: %q", body[:min(80, len(body))])
	}
	if body != strings.TrimPrefix(withHeader, "⏰ <b>Матчи ваших команд на 24 часа:</b>\n\n") {
		t.Error("тело с заголовком и без должно совпадать")
	}
	if !strings.Contains(body, "📅 <b>Матчи:</b>") {
		t.Error("нет блока предстоящих")
	}
	if !strings.Contains(body, "(BO3)") {
		t.Error("ожидали суффикс BO3 у предстоящего")
	}

	noLive := formatMatchesMessage("", nil, upcoming, subs, 3, 3)
	if strings.Contains(noLive, "Сейчас играют") {
		t.Error("live-блок не должен появляться без live-матчей")
	}
	onlyLive := formatMatchesMessage("", live, nil, subs, 3, 3)
	if strings.Contains(onlyLive, "📅 <b>Матчи:</b>") {
		t.Error("блок предстоящих не должен появляться без upcoming")
	}
}

func TestFormatMatchesMessageHighlightsSubs(t *testing.T) {
	subs := []domain.TeamInfo{{ID: 1, Name: "G2"}}
	upcoming := []domain.Match{{
		ID: 11, TeamA: "G2", TeamB: "FaZe", TeamAID: 1, TeamBID: 3,
		Time: time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC),
		Tournament: "EPL", TournamentID: 7,
		TournamentBeginAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC),
	}}
	got := formatMatchesMessage("", nil, upcoming, subs, 3, 3)
	t.Logf("out=%q", got)
	if !strings.Contains(got, "<b>G2</b>") {
		t.Fatalf("ожидали жирный G2, got %q", got)
	}
	if strings.Contains(got, "<b>FaZe</b>") {
		t.Fatalf("неподписанная команда не должна быть жирной: %q", got)
	}
}
