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
		Time:          time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC),
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
		Time:       time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC),
		Tournament: "EPL", TournamentID: 7,
		TournamentBeginAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC),
	}}
	got := formatMatchesMessage("", nil, upcoming, subs, 3, 3)
	t.Logf("out=%q", got)
	if !strings.Contains(got, "<b>G2</b>") {
		t.Fatalf("ожидали жирный G2, got %q", got)
	}
	if strings.Contains(got, "<u>") {
		t.Fatalf("интересная команда без подчёркивания: %q", got)
	}
	if strings.Contains(got, "<b>FaZe</b>") {
		t.Fatalf("неподписанная команда не должна быть жирной: %q", got)
	}
}

func TestFormatMatchesMessageStarsFavorite(t *testing.T) {
	subs := []domain.TeamInfo{
		{ID: 1, Name: "G2", Favorite: true},
		{ID: 3, Name: "FaZe", Favorite: false},
	}
	upcoming := []domain.Match{{
		ID: 11, TeamA: "G2", TeamB: "FaZe", TeamAID: 1, TeamBID: 3,
		Time:       time.Date(2026, 10, 8, 15, 0, 0, 0, time.UTC),
		Tournament: "EPL", TournamentID: 7,
		TournamentBeginAt: time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC),
	}}
	got := formatMatchesMessage("", nil, upcoming, subs, 3, 3)
	if !strings.Contains(got, "<u><b>G2</b></u>") {
		t.Fatalf("ожидали подчёркнутый жирный G2, got %q", got)
	}
	if !strings.Contains(got, "<b>FaZe</b>") {
		t.Fatalf("интересная должна остаться жирной, got %q", got)
	}
	if strings.Contains(got, "<u><b>FaZe</b></u>") {
		t.Fatalf("подчёркивание только у любимой: %q", got)
	}
}

func TestFormatMatchCard(t *testing.T) {
	m := domain.Match{
		TeamA: "Nemiga", TeamB: "FURIA",
		NumberOfGames: 5,
		Tournament:    "PGL Masters Bucharest 2026",
		Time:          time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC),
		Streams: []domain.MatchStream{
			{URL: "https://www.twitch.tv/PGL", Official: true},
			{URL: "https://www.youtube.com/watch?v=DTnENvX1JV0", Official: true, Channel: "PGL"},
			{URL: "https://www.twitch.tv/bysl4m"},
		},
	}
	got := formatMatchCard(m, "<b>Nemiga</b>", "<b>FURIA</b>", true, nil)
	if !strings.HasPrefix(got, "<b>Nemiga</b> vs <b>FURIA</b> (BO5)\n<blockquote>") {
		t.Fatalf("title outside blockquote: %q", got)
	}
	if !strings.HasSuffix(got, "</blockquote>") {
		t.Fatalf("details must close blockquote: %q", got)
	}
	inner := got[strings.Index(got, "<blockquote>")+len("<blockquote>") : strings.LastIndex(got, "</blockquote>")]
	for _, want := range []string{
		"Турнир:",
		"PGL Masters Bucharest 2026",
		"Трансляция:",
		"PGL (Twitch)",
		"PGL (YouTube)",
		"Найти трансляцию:",
		"HLTV: ",
		"Профиль матча",
	} {
		if !strings.Contains(inner, want) {
			t.Errorf("missing %q in blockquote %q", want, inner)
		}
	}
	for _, ban := range []string{"⏰", "📺", "🎮", "⭐", "🔍", "📊", "🏟", "bysl4m", "Время:"} {
		if strings.Contains(got, ban) {
			t.Errorf("unexpected %q in %q", ban, got)
		}
	}
}

func TestFormatMatchCardNoOfficialNoTournament(t *testing.T) {
	m := domain.Match{TeamA: "A", TeamB: "B"}
	got := formatMatchCard(m, "A", "B", true, nil)
	if strings.Contains(got, "Турнир:") {
		t.Errorf("empty tournament must be skipped: %q", got)
	}
	if strings.Contains(got, "Трансляция:") {
		t.Errorf("no official line without streams: %q", got)
	}
	if !strings.Contains(got, "<blockquote>Найти трансляцию:") {
		t.Errorf("fallback inside blockquote: %q", got)
	}
}

func TestFormatMatchCardTBDNoHLTV(t *testing.T) {
	m := domain.Match{TeamA: "TBD", TeamB: "FURIA", Tournament: "EPL"}
	got := formatMatchCard(m, "TBD", "FURIA", true, nil)
	if strings.Contains(got, "HLTV:") {
		t.Errorf("TBD must skip HLTV: %q", got)
	}
	if !strings.Contains(got, "Турнир:") {
		t.Errorf("tournament still shown: %q", got)
	}
}

func TestFormatMatchCardSkipTournament(t *testing.T) {
	m := domain.Match{TeamA: "A", TeamB: "B", Tournament: "EPL"}
	got := formatMatchCard(m, "A", "B", false, nil)
	if strings.Contains(got, "Турнир:") {
		t.Errorf("withTournament=false must skip: %q", got)
	}
}

func TestFormatMatchCardMiddleLines(t *testing.T) {
	m := domain.Match{TeamA: "A", TeamB: "B"}
	got := formatMatchCard(m, "A", "B", true, []string{"Серия: A 1 — 0 B", "Идет: 12 мин"})
	if !strings.HasPrefix(got, "A vs B\n<blockquote>") {
		t.Fatalf("title outside blockquote: %q", got)
	}
	inner := got[strings.Index(got, "<blockquote>")+len("<blockquote>") : strings.LastIndex(got, "</blockquote>")]
	if !strings.Contains(inner, "Серия: A 1 — 0 B\nИдет: 12 мин\nНайти трансляцию:") {
		t.Errorf("middle lines inside blockquote before streams: %q", inner)
	}
}
