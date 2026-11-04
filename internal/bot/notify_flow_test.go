package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

type flowStore struct {
	fans          []domain.MatchFan
	tournamentErr error
	offsets       map[int64]int
	offsetErr     error
	tourneyErr    error
	marked        bool
}

func (s *flowStore) HasTournamentSubscriptions(int64) (bool, error) {
	return false, s.tourneyErr
}
func (s *flowStore) MarkDigestSent(int64, string) error {
	s.marked = true
	return nil
}
func (s *flowStore) GetMatchFans(int, int) ([]domain.MatchFan, error) { return s.fans, nil }
func (s *flowStore) GetUsersByTournamentID(int) ([]int64, error) {
	return nil, s.tournamentErr
}
func (s *flowStore) GetUserOffsets([]int64) (map[int64]int, error) {
	return s.offsets, s.offsetErr
}
func (s *flowStore) FillStreamChannels([]domain.Match) {}

func (s *flowStore) GetUserSubscriptions(int64) ([]domain.TeamInfo, error) { return nil, nil }
func (s *flowStore) GetSubscribedTeamIDs() ([]string, error)               { return nil, nil }
func (s *flowStore) GetScheduleMatches(int64, int64, bool) ([]domain.Match, []domain.Match, error) {
	return nil, nil, nil
}
func (s *flowStore) GetScoreMatches(int64, bool) ([]domain.Match, error) { return nil, nil }
func (s *flowStore) GetMatchesForReminder() ([]domain.Match, error)      { return nil, nil }
func (s *flowStore) ProcessMatch(domain.Match) (domain.MatchChange, error) {
	return domain.MatchChange{}, nil
}
func (s *flowStore) MarkMatchAsNotified(int, time.Time) (bool, error) { return false, nil }
func (s *flowStore) CleanOldMatches()                                 {}
func (s *flowStore) CleanStaleRunningMatches(map[int]bool)            {}
func (s *flowStore) Subscribe(int64, int64, string, bool) error       { return nil }
func (s *flowStore) Unsubscribe(int64, int64) error                   { return nil }
func (s *flowStore) RemoveUser(int64) error                           { return nil }
func (s *flowStore) GetStats() (domain.BotStats, error)               { return domain.BotStats{}, nil }
func (s *flowStore) SearchTeams(string) ([]domain.SearchedTeam, error) {
	return nil, nil
}
func (s *flowStore) GetTeamsByIDs([]int) ([]domain.TeamInfo, error) { return nil, nil }
func (s *flowStore) GetDigestSettings(int64) (domain.DigestSettings, error) {
	return domain.DigestSettings{}, nil
}
func (s *flowStore) SetDigestEnabled(int64, bool) error           { return nil }
func (s *flowStore) SetDigestHour(int64, int) error               { return nil }
func (s *flowStore) GetUserOffset(int64) (int, error)             { return 0, nil }
func (s *flowStore) SetUserOffset(int64, int) error               { return nil }
func (s *flowStore) SetOffsetAndDigestHour(int64, int, int) error { return nil }
func (s *flowStore) GetDigestDueUsers(int, string) ([]domain.DigestDueUser, error) {
	return nil, nil
}
func (s *flowStore) GetHLTVURLs([]int) (map[int]string, error) { return nil, nil }
func (s *flowStore) GetTournamentsForUser(int64) ([]domain.TournamentInfo, error) {
	return nil, nil
}
func (s *flowStore) SubscribeTournament(int64, int) error   { return nil }
func (s *flowStore) UnsubscribeTournament(int64, int) error { return nil }
func (s *flowStore) GetActiveSubscribedTournamentIDs() ([]string, error) {
	return nil, nil
}
func (s *flowStore) CleanFinishedTournaments() {}

func TestDigestDoesNotMarkWhenTournamentLookupFails(t *testing.T) {
	st := &flowStore{tourneyErr: errors.New("db")}
	b := &Bot{storage: st}
	if b.processDigestUser(context.Background(), 1, 3, time.Now(), "slot") {
		t.Fatal("digest reported sent")
	}
	if st.marked {
		t.Fatal("digest slot was marked sent")
	}
}

func TestTournamentLookupErrorKeepsTeamFans(t *testing.T) {
	st := &flowStore{
		fans:          []domain.MatchFan{{UserID: 7, FavoriteA: true}},
		tournamentErr: errors.New("db"),
	}
	b := &Bot{storage: st}
	got, err := b.matchEventRecipients(matchEvent{
		kind: domain.EventStarted,
		match: domain.Match{
			TeamA: "A", TeamB: "B", IsPlayoff: true, TournamentID: 4,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].UserID != 7 {
		t.Fatalf("fans = %+v", got)
	}
}

func TestUndeliveredPushIsRetriedNextCycle(t *testing.T) {
	st := &flowStore{
		fans:    []domain.MatchFan{{UserID: 9, FavoriteA: true}},
		offsets: map[int64]int{9: 3},
	}
	b := &Bot{
		storage:      st,
		broadcastCh:  make(chan BroadcastTask),
		reminderSent: map[reminderKey]map[int64]struct{}{},
	}
	ev := []matchEvent{{
		kind: domain.EventNew,
		match: domain.Match{
			ID: 1, TeamA: "A", TeamB: "B",
			Time: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		},
	}}
	b.broadcastMatchEvents(context.Background(), ev)
	if len(b.heldPushes) != 1 || b.heldPushes[0].userID != 9 {
		t.Fatalf("held = %+v", b.heldPushes)
	}

	b.broadcastCh = make(chan BroadcastTask, 1)
	b.broadcastMatchEvents(context.Background(), nil)
	select {
	case task := <-b.broadcastCh:
		if task.UserID != 9 || task.Text == "" {
			t.Fatalf("retry task = %+v", task)
		}
	default:
		t.Fatal("held push was not retried")
	}
	if len(b.heldPushes) != 0 {
		t.Fatalf("held left = %+v", b.heldPushes)
	}
}
