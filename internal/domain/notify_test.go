package domain

import "testing"

func TestClassifyMatchUpdate(t *testing.T) {
	tests := []struct {
		name                                                            string
		isNew, timeChanged, minorTimeShift, teamsChanged, statusChanged bool
		status, oldStatus                                               string
		wantKind                                                        EventKind
		wantOK                                                          bool
	}{
		{name: "new", isNew: true, status: StatusNotStarted, wantKind: EventNew, wantOK: true},
		{name: "new postponed", isNew: true, status: StatusPostponed, wantOK: false},
		{name: "new finished silent", isNew: true, status: StatusFinished, wantOK: false},
		{name: "finished opponent silent", teamsChanged: true, status: StatusFinished, wantOK: false},
		{name: "finished time silent", timeChanged: true, status: StatusFinished, wantOK: false},
		{name: "started", statusChanged: true, status: StatusRunning, oldStatus: StatusNotStarted, wantKind: EventStarted, wantOK: true},
		{name: "opponent", teamsChanged: true, status: StatusNotStarted, wantKind: EventOpponent, wantOK: true},
		{name: "time", timeChanged: true, status: StatusPostponed, oldStatus: StatusNotStarted, wantKind: EventTimeChanged, wantOK: true},
		{name: "minor time only", timeChanged: true, minorTimeShift: true, status: StatusNotStarted, wantOK: false},
		{name: "minor time with opponent", timeChanged: true, minorTimeShift: true, teamsChanged: true, status: StatusNotStarted, wantKind: EventOpponent, wantOK: true},
		{name: "postponed status only", statusChanged: true, status: StatusPostponed, oldStatus: StatusNotStarted, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := ClassifyMatchUpdate(MatchChange{
				IsNew: tt.isNew, TimeChanged: tt.timeChanged, MinorTimeShift: tt.minorTimeShift,
				TeamsChanged: tt.teamsChanged, StatusChanged: tt.statusChanged, OldStatus: tt.oldStatus,
			}, tt.status)
			if ok != tt.wantOK || (ok && kind != tt.wantKind) {
				t.Fatalf("ClassifyMatchUpdate() = (%v, %v), want (%v, %v)", kind, ok, tt.wantKind, tt.wantOK)
			}
		})
	}
}

func TestFansForMatchEvent(t *testing.T) {
	fans := []MatchFan{
		{UserID: 1, FavoriteA: true},
		{UserID: 2},
		{UserID: 3, FavoriteB: true},
	}
	started := FansForMatchEvent(EventStarted, fans)
	if len(started) != 3 {
		t.Fatalf("start recipients = %d, want 3", len(started))
	}
	for _, kind := range []EventKind{EventNew, EventOpponent, EventTimeChanged} {
		got := FansForMatchEvent(kind, fans)
		if len(got) != 2 || !got[0].HasFavorite() || !got[1].HasFavorite() {
			t.Fatalf("kind %d recipients = %+v, want only favorites", kind, got)
		}
	}
}
