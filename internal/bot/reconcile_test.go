package bot

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

type reconcilePanda struct {
	teamMatches, serieMatches    []domain.Match
	teamErr, serieErr, directErr error
	direct                       *domain.Match
	verified                     map[int]domain.Match
	checked                      [][]int
	used                         int
}

func (p *reconcilePanda) FetchMatchesByTeamIDs(context.Context, []string, time.Time) ([]domain.Match, error) {
	p.used++
	return p.teamMatches, p.teamErr
}

func (p *reconcilePanda) FetchMatchesBySerieIDs(context.Context, []string, time.Time) ([]domain.Match, error) {
	p.used++
	return p.serieMatches, p.serieErr
}

func (p *reconcilePanda) FetchMatchesByIDs(_ context.Context, ids []int) (map[int]domain.Match, error) {
	p.used++
	p.checked = append(p.checked, append([]int(nil), ids...))
	if p.directErr != nil || p.direct == nil {
		if p.verified != nil {
			return p.verified, nil
		}
		return nil, p.directErr
	}
	return map[int]domain.Match{p.direct.ID: *p.direct}, nil
}

func TestReconcileUpcomingMatchesUsesSingleBatch(t *testing.T) {
	bot, st := newTournamentTestBot(t)
	verified := make(map[int]domain.Match)
	for id := 1; id <= 2; id++ {
		m := domain.Match{ID: id, TeamA: "A", TeamB: "B", TeamAID: 10, TeamBID: 20,
			Time: time.Now().Add(time.Hour), Status: "not_started"}
		if _, err := st.ProcessMatch(m); err != nil {
			t.Fatal(err)
		}
		verified[id] = m
	}
	p := &reconcilePanda{verified: verified}
	bot.panda = p
	got := bot.reconcileUpcomingMatches(context.Background(), nil)
	if len(p.checked) != 1 || len(p.checked[0]) != 2 {
		t.Fatalf("пакетные вызовы = %v, want один вызов с двумя ID", p.checked)
	}
	if len(got) != 2 {
		t.Fatalf("подтверждённых матчей = %d, want 2", len(got))
	}
}

func (p *reconcilePanda) APIUsage() (int, int, int) { return p.used, 900 - p.used, 1000 }

func TestPollerReconcilesMissingUpcoming(t *testing.T) {
	apiErr := errors.New("API unavailable")
	for _, tt := range []struct {
		name, source                               string
		a, b                                       int
		teamErr, serieErr, directErr               error
		missing, wantDelete, wantCheck, wantUpdate bool
	}{
		{name: "replaced opponent", a: 1, b: 3, wantDelete: true, wantCheck: true},
		{name: "canceled or 404", missing: true, wantDelete: true, wantCheck: true},
		{name: "same participants", a: 1, b: 2, wantCheck: true, wantUpdate: true},
		{name: "swapped participants", a: 2, b: 1, wantCheck: true, wantUpdate: true},
		{name: "direct request failed", directErr: apiErr, wantCheck: true},
		{name: "team polling failed", teamErr: apiErr},
		{name: "series polling failed", serieErr: apiErr},
		{name: "replacement in team response", source: "teams", a: 3, b: 2, wantUpdate: true},
		{name: "replacement in series response", source: "series", a: 1, b: 3, wantUpdate: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			bot, st := newTournamentTestBot(t)
			bot.broadcastCh = make(chan BroadcastTask, 10)
			if err := st.Subscribe(1, 2, "B", true); err != nil {
				t.Fatal(err)
			}
			old := domain.Match{ID: 1789260, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2,
				Time: time.Now().Add(3 * time.Minute), Status: "not_started", IsPlayoff: true,
				TournamentID: 7, Tournament: "Tournament", TournamentEndAt: time.Now().Add(24 * time.Hour)}
			if _, err := st.ProcessMatch(old); err != nil {
				t.Fatal(err)
			}
			if err := st.SubscribeTournament(1, 7); err != nil {
				t.Fatal(err)
			}
			current := old
			current.TeamAID, current.TeamBID = tt.a, tt.b
			current.TeamA, current.TeamB = fmt.Sprint(tt.a), fmt.Sprint(tt.b)
			current.StageName = "Playoffs"
			p := &reconcilePanda{teamErr: tt.teamErr, serieErr: tt.serieErr, directErr: tt.directErr}
			switch tt.source {
			case "teams":
				p.teamMatches = []domain.Match{current}
			case "series":
				p.serieMatches = []domain.Match{current}
			default:
				if !tt.missing {
					p.direct = &current
				}
			}
			bot.panda = p
			delay := bot.runPollerCycle(context.Background(), time.Minute)
			if (len(p.checked) == 1) != tt.wantCheck || (len(p.checked) > 0 && (len(p.checked[0]) != 1 || p.checked[0][0] != old.ID)) {
				t.Fatalf("проверки по ID = %v, wantCheck %v", p.checked, tt.wantCheck)
			}
			if delay != time.Duration(p.used)*time.Hour/500 {
				t.Errorf("дополнительный запрос не учтён в темпе: delay=%v used=%d", delay, p.used)
			}
			stats, err := st.GetStats()
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 1
			if tt.wantDelete {
				wantCount = 0
			}
			if stats.Matches != wantCount {
				t.Fatalf("матчей в БД: %d, want %d", stats.Matches, wantCount)
			}
			_, upcoming, err := st.GetScheduleMatches(1, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			reminders, err := st.GetMatchesForReminder()
			if err != nil {
				t.Fatal(err)
			}
			if len(upcoming) != wantCount || len(reminders) != wantCount {
				t.Fatalf("расписание=%d напоминания=%d, want %d", len(upcoming), len(reminders), wantCount)
			}
			if len(upcoming) > 0 {
				want := old
				if tt.wantUpdate {
					want = current
				}
				if got := upcoming[0]; got.TeamAID != want.TeamAID || got.TeamBID != want.TeamBID || got.StageName != want.StageName {
					t.Errorf("неверная запись после сверки: %+v", got)
				}
			}
			if tt.wantDelete && len(bot.broadcastCh) != 0 {
				t.Error("удаление не должно рассылать уведомления")
			}
			if tt.source == "teams" && len(bot.broadcastCh) != 1 {
				t.Error("обычное обновление должно уведомить о смене соперника")
			}
		})
	}
}
