package bot

import (
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func TestClassifyMatchUpdate(t *testing.T) {
	tests := []struct {
		name                                                            string
		isNew, timeChanged, minorTimeShift, teamsChanged, statusChanged bool
		status, oldStatus                                               string
		wantKind                                                        matchEventKind
		wantOK                                                          bool
	}{
		{name: "new", isNew: true, status: "not_started", wantKind: eventNew, wantOK: true},
		{name: "new postponed", isNew: true, status: "postponed", wantOK: false},
		{name: "new finished silent", isNew: true, status: "finished", wantOK: false},
		{name: "finished opponent silent", teamsChanged: true, status: "finished", wantOK: false},
		{name: "finished time silent", timeChanged: true, status: "finished", wantOK: false},
		{name: "started", statusChanged: true, status: "running", oldStatus: "not_started", wantKind: eventStarted, wantOK: true},
		{name: "opponent", teamsChanged: true, status: "not_started", wantKind: eventOpponent, wantOK: true},
		{name: "time", timeChanged: true, status: "postponed", oldStatus: "not_started", wantKind: eventTimeChanged, wantOK: true},
		{name: "minor time only", timeChanged: true, minorTimeShift: true, status: "not_started", wantOK: false},
		{name: "minor time with opponent", timeChanged: true, minorTimeShift: true, teamsChanged: true, status: "not_started", wantKind: eventOpponent, wantOK: true},
		{name: "postponed status only", statusChanged: true, status: "postponed", oldStatus: "not_started", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := classifyMatchUpdate(domain.MatchChange{
				IsNew: tt.isNew, TimeChanged: tt.timeChanged, MinorTimeShift: tt.minorTimeShift,
				TeamsChanged: tt.teamsChanged, StatusChanged: tt.statusChanged, OldStatus: tt.oldStatus,
			}, tt.status)
			if ok != tt.wantOK || (ok && kind != tt.wantKind) {
				t.Fatalf("classifyMatchUpdate() = (%v, %v), want (%v, %v)", kind, ok, tt.wantKind, tt.wantOK)
			}
		})
	}
}

func TestResolvePollSince(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	floor := now.Add(-pollHistoryWindow)

	if got := resolvePollSince(time.Time{}, now); !got.Equal(floor) {
		t.Fatalf("zero cursor: got %v, want floor %v", got, floor)
	}
	old := now.Add(-48 * time.Hour)
	if got := resolvePollSince(old, now); !got.Equal(floor) {
		t.Fatalf("cursor older than floor: got %v, want floor %v", got, floor)
	}
	fresh := now.Add(-2 * time.Hour)
	if got := resolvePollSince(fresh, now); !got.Equal(fresh) {
		t.Fatalf("fresh cursor: got %v, want %v", got, fresh)
	}
}

func TestEarliestUnfinishedBegin(t *testing.T) {
	t1 := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)

	got, ok := earliestUnfinishedBegin([]domain.Match{
		{ID: 1, Status: "finished", Time: t3},
		{ID: 2, Status: "running", Time: t1},
		{ID: 3, Status: "not_started", Time: t2},
		{ID: 4, Status: "postponed", Time: time.Time{}},
	})
	if !ok || !got.Equal(t2) {
		t.Fatalf("earliestUnfinishedBegin() = (%v, %v), want (%v, true)", got, ok, t2)
	}

	_, ok = earliestUnfinishedBegin([]domain.Match{
		{ID: 1, Status: "finished", Time: t1},
	})
	if ok {
		t.Fatal("expected no unfinished matches")
	}
}

func TestFansForMatchEvent(t *testing.T) {
	fans := []domain.MatchFan{
		{UserID: 1, FavoriteA: true},
		{UserID: 2},
		{UserID: 3, FavoriteB: true},
	}
	started := fansForMatchEvent(eventStarted, fans)
	if len(started) != 3 {
		t.Fatalf("start recipients = %d, want 3", len(started))
	}
	for _, kind := range []matchEventKind{eventNew, eventOpponent, eventTimeChanged} {
		got := fansForMatchEvent(kind, fans)
		if len(got) != 2 || !got[0].HasFavorite() || !got[1].HasFavorite() {
			t.Fatalf("kind %d recipients = %+v, want only favorites", kind, got)
		}
	}
}

func TestBuildCombinedUpdateMessageStarsFavorite(t *testing.T) {
	ev := matchEvent{
		kind: eventNew,
		match: domain.Match{
			TeamA: "G2", TeamB: "NAVI", TeamAID: 1, TeamBID: 2,
			Time: time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC),
		},
		favA: true,
	}
	got := buildCombinedUpdateMessage([]matchEvent{ev}, 3, 0)
	if !strings.Contains(got, "<u><b>G2</b></u>") {
		t.Fatalf("нет подчёркивания у любимой: %q", got)
	}
	if !strings.Contains(got, "<b>NAVI</b>") || strings.Contains(got, "<u><b>NAVI</b></u>") {
		t.Fatalf("чужая сторона жирная и без подчёркивания: %q", got)
	}
}

func TestTeamIDsKey(t *testing.T) {
	if got := teamIDsKey([]string{"3", "1", "2"}); got != "1,2,3" {
		t.Fatalf("teamIDsKey sorted = %q, want 1,2,3", got)
	}
	if teamIDsKey(nil) != "" {
		t.Fatal("empty key for nil")
	}
}

func TestPollCursorTeamReset(t *testing.T) {
	b := &Bot{}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	b.pollCursor = now.Add(-2 * time.Hour)
	b.pollCursorTeams = pollScopeKey([]string{"1", "2"}, nil)

	since := b.pollSinceForTeams([]string{"1", "2"}, nil, now)
	if !since.Equal(b.pollCursor) {
		t.Fatalf("same teams should keep cursor: since=%v cursor=%v", since, b.pollCursor)
	}

	since = b.pollSinceForTeams([]string{"1", "2", "3"}, nil, now)
	floor := now.Add(-pollHistoryWindow)
	if !since.Equal(floor) {
		t.Fatalf("team change should reset to floor: since=%v want %v", since, floor)
	}
	if !b.pollCursor.IsZero() {
		t.Fatalf("cursor should be cleared after team change, got %v", b.pollCursor)
	}
}

func TestRememberPollCursor(t *testing.T) {
	b := &Bot{}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	early := now.Add(-3 * time.Hour)

	b.rememberPollCursor([]domain.Match{
		{Status: "finished", Time: now.Add(-5 * time.Hour)},
		{Status: "running", Time: early},
		{Status: "not_started", Time: now.Add(time.Hour)},
	}, now)
	if !b.pollCursor.Equal(early) {
		t.Fatalf("cursor=%v, want earliest unfinished %v", b.pollCursor, early)
	}

	b.rememberPollCursor([]domain.Match{
		{Status: "finished", Time: now.Add(-time.Hour)},
	}, now)
	if !b.pollCursor.Equal(now) {
		t.Fatalf("no unfinished: cursor=%v, want now %v", b.pollCursor, now)
	}
}
