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
	if err := st.Subscribe(1, 10, "Fan"); err != nil {
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
		{"старт плей-офф: фанат и подписчик турнира без дублей", matchEvent{match: playoff, kind: eventStarted}, []int64{1, 2}},
		{"новый плей-офф матч: только фанат", matchEvent{match: playoff, kind: eventNew}, []int64{1}},
		{"смена времени: только фанат", matchEvent{match: playoff, kind: eventTimeChanged}, []int64{1}},
		{"старт группового: только фанат", matchEvent{match: group, kind: eventStarted}, []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			users, err := b.matchEventRecipients(tt.ev)
			if err != nil {
				t.Fatal(err)
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
	if err := st.Subscribe(1, 10, "Fan"); err != nil {
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

	text, extra, err := b.buildScheduleView(1, false)
	if err != nil || extra != 0 || strings.Contains(text, "Nemiga") {
		t.Errorf("без подписки на турнир: extra=%d err=%v text=%q", extra, err, text)
	}
	if viewToggleMarkup(viewSchedule, false, extra) != nil {
		t.Error("без турнирных матчей кнопки быть не должно")
	}

	if err := st.SubscribeTournament(1, 500); err != nil {
		t.Fatal(err)
	}
	text, extra, err = b.buildScheduleView(1, false)
	if err != nil || extra != 1 || strings.Contains(text, "Nemiga") || !strings.Contains(text, "Fan") {
		t.Errorf("скрытый режим: extra=%d err=%v text=%q", extra, err, text)
	}
	markup := viewToggleMarkup(viewSchedule, false, extra)
	if markup == nil || markup.InlineKeyboard[0][0].Text != "🏆 Показать турнирные матчи (1)" {
		t.Errorf("кнопка показа: %+v", markup)
	}

	text, extra, err = b.buildScheduleView(1, true)
	if err != nil || extra != 1 || !strings.Contains(text, "Nemiga") {
		t.Errorf("полный режим: extra=%d err=%v text=%q", extra, err, text)
	}
	if strings.Contains(text, "<b>Nemiga</b>") {
		t.Error("чужие команды не должны подсвечиваться")
	}
	if markup := viewToggleMarkup(viewSchedule, true, extra); markup == nil || markup.InlineKeyboard[0][0].Text != "🙈 Скрыть турнирные матчи" {
		t.Errorf("кнопка скрытия: %+v", markup)
	}

	// Только подписка на турнир, без команд — расписание все равно строится.
	if err := st.SubscribeTournament(2, 500); err != nil {
		t.Fatal(err)
	}
	text, extra, err = b.buildScheduleView(2, false)
	if err != nil || extra != 1 || text != "Для ваших команд в ближайшее время игр не найдено." {
		t.Errorf("только турнир: extra=%d err=%v text=%q", extra, err, text)
	}
}

func TestTournamentsMenu(t *testing.T) {
	b, st := newTournamentTestBot(t)
	if err := st.Subscribe(1, 10, "Fan"); err != nil {
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
	if !strings.Contains(text, "только матчи ваших команд") {
		t.Errorf("текст режима по умолчанию: %q", text)
	}
	if len(markup.InlineKeyboard) != 2 {
		t.Fatalf("строк клавиатуры = %d, want 2", len(markup.InlineKeyboard))
	}
	if got := markup.InlineKeyboard[0][0].Text; got != "✅ PGL Masters Bucharest 2026" {
		t.Errorf("кнопка турнира = %q", got)
	}
	if got := markup.InlineKeyboard[1][0].Text; got != "🙈 По умолчанию: только мои команды" {
		t.Errorf("кнопка режима = %q", got)
	}
}
