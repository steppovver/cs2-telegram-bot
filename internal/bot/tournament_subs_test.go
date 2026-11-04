package bot

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
	"cs2bot/internal/storage"
)

func newTournamentTestBot(t *testing.T) (*Bot, *storage.Storage) {
	t.Helper()
	st, err := storage.NewStorage(filepath.Join(t.TempDir(), "tournaments.db"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &Bot{storage: st}, st
}

func TestMergePlayoffMatches(t *testing.T) {
	team := []domain.Match{{ID: 1}, {ID: 2, IsPlayoff: true}}
	serie := []domain.Match{{ID: 2, IsPlayoff: true}, {ID: 3}, {ID: 4, IsPlayoff: true}}
	var ids []int
	for _, m := range mergePlayoffMatches(team, serie) {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []int{1, 2, 4}) {
		t.Errorf("mergePlayoffMatches = %v, want [1 2 4]", ids)
	}
}

func TestMatchEventRecipients(t *testing.T) {
	b, st := newTournamentTestBot(t)
	if err := st.Subscribe(1, 10, "Fan", false); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe(3, 10, "Fav", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SubscribeTournament(1, 500); err != nil {
		t.Fatal(err)
	}
	if err := st.SubscribeTournament(2, 500); err != nil {
		t.Fatal(err)
	}

	playoff := domain.Match{TeamAID: 10, TeamBID: 20, TournamentID: 500, IsPlayoff: true}
	group := playoff
	group.IsPlayoff = false

	tests := []struct {
		name  string
		ev    matchEvent
		users []int64
	}{
		{"старт плей-офф: интересная, любимая и турнир без дублей", matchEvent{match: playoff, kind: domain.EventStarted}, []int64{1, 2, 3}},
		{"новый плей-офф матч: только любимая", matchEvent{match: playoff, kind: domain.EventNew}, []int64{3}},
		{"смена времени: только любимая", matchEvent{match: playoff, kind: domain.EventTimeChanged}, []int64{3}},
		{"старт группового: интересная и любимая", matchEvent{match: group, kind: domain.EventStarted}, []int64{1, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fans, err := b.matchEventRecipients(tt.ev)
			if err != nil {
				t.Fatal(err)
			}
			users := make([]int64, len(fans))
			for i, f := range fans {
				users[i] = f.UserID
				if f.UserID == 3 && !f.FavoriteA {
					t.Error("любимая команда не помечена")
				}
				if f.UserID == 1 && f.HasFavorite() {
					t.Error("интересная команда помечена как любимая")
				}
			}
			slices.Sort(users)
			if !slices.Equal(users, tt.users) {
				t.Errorf("получатели = %v, want %v", users, tt.users)
			}
		})
	}
}

func TestBuildScheduleViewTournaments(t *testing.T) {
	b, st := newTournamentTestBot(t)
	now := time.Now()
	if err := st.Subscribe(1, 10, "Fan", false); err != nil {
		t.Fatal(err)
	}
	for _, m := range []domain.Match{
		{ID: 1, TeamA: "Fan", TeamB: "Rival", TeamAID: 10, TeamBID: 20, Time: now.Add(time.Hour), Status: "not_started",
			TournamentID: 500, Tournament: "PGL Masters Bucharest 2026", TournamentEndAt: now.Add(48 * time.Hour)},
		{ID: 2, TeamA: "Nemiga", TeamB: "FURIA", TeamAID: 30, TeamBID: 40, Time: now.Add(2 * time.Hour), Status: "not_started",
			TournamentID: 500, Tournament: "PGL Masters Bucharest 2026", TournamentEndAt: now.Add(48 * time.Hour), IsPlayoff: true},
	} {
		if _, err := st.ProcessMatch(m); err != nil {
			t.Fatal(err)
		}
	}

	text, err := b.buildScheduleView(1)
	if err != nil || strings.Contains(text, "Nemiga") || !strings.Contains(text, "Fan") {
		t.Errorf("без подписки на турнир: err=%v text=%q", err, text)
	}

	if err := st.SubscribeTournament(1, 500); err != nil {
		t.Fatal(err)
	}
	text, err = b.buildScheduleView(1)
	if err != nil || !strings.Contains(text, "Nemiga") || !strings.Contains(text, "Fan") {
		t.Errorf("с подпиской на турнир: err=%v text=%q", err, text)
	}
	if strings.Contains(text, "<b>Nemiga</b>") {
		t.Error("чужие команды не должны подсвечиваться")
	}

	if err := st.SubscribeTournament(2, 500); err != nil {
		t.Fatal(err)
	}
	text, err = b.buildScheduleView(2)
	if err != nil || !strings.Contains(text, "Nemiga") {
		t.Errorf("только турнир: err=%v text=%q", err, text)
	}
}

func TestTournamentsMenu(t *testing.T) {
	b, st := newTournamentTestBot(t)
	if err := st.Subscribe(1, 10, "Fan", false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ProcessMatch(domain.Match{
		ID: 1, TeamA: "Fan", TeamB: "Rival", TeamAID: 10, TeamBID: 20, Time: time.Now(), Status: "finished",
		TournamentID: 500, Tournament: "PGL Masters Bucharest 2026",
		TournamentBeginAt: time.Now().Add(-24 * time.Hour), TournamentEndAt: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SubscribeTournament(1, 500); err != nil {
		t.Fatal(err)
	}

	text, markup, err := b.buildTournamentsMenu(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "уведомление о начале такого матча") {
		t.Errorf("текст уведомлений: %q", text)
	}
	if strings.Contains(text, "Выключить уведомления") || strings.Contains(text, "Включить уведомления") {
		t.Errorf("текст тумблера остался: %q", text)
	}
	if !strings.Contains(text, "в расписание, счет и дайджест") {
		t.Errorf("текст расписания: %q", text)
	}
	if len(markup.InlineKeyboard) != 1 {
		t.Fatalf("строк клавиатуры = %d, want 1", len(markup.InlineKeyboard))
	}
	if got := markup.InlineKeyboard[0][0].Text; got != "✅ PGL Masters Bucharest 2026" {
		t.Errorf("кнопка турнира = %q", got)
	}

	emptyText, emptyMarkup, err := b.buildTournamentsMenu(2)
	if err != nil {
		t.Fatal(err)
	}
	if emptyMarkup != nil {
		t.Fatalf("пустой список: клавиатура = %#v", emptyMarkup)
	}
	if !strings.Contains(emptyText, "Пока нет турниров") {
		t.Errorf("пустой список: %q", emptyText)
	}
}
