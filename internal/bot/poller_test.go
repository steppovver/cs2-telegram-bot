package bot

import (
	"testing"

	"cs2bot/internal/domain"
)

func TestClassifyMatchUpdate(t *testing.T) {
	tests := []struct {
		name                                            string
		isNew, timeChanged, teamsChanged, statusChanged bool
		status, oldStatus                               string
		wantKind                                        matchEventKind
		wantOK                                          bool
	}{
		{name: "new", isNew: true, status: "not_started", wantKind: eventNew, wantOK: true},
		{name: "new postponed", isNew: true, status: "postponed", wantOK: false},
		{name: "started", statusChanged: true, status: "running", oldStatus: "not_started", wantKind: eventStarted, wantOK: true},
		{name: "opponent", teamsChanged: true, status: "not_started", wantKind: eventOpponent, wantOK: true},
		{name: "time", timeChanged: true, status: "postponed", oldStatus: "not_started", wantKind: eventTimeChanged, wantOK: true},
		{name: "postponed status only", statusChanged: true, status: "postponed", oldStatus: "not_started", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, ok := classifyMatchUpdate(domain.MatchChange{
				IsNew: tt.isNew, TimeChanged: tt.timeChanged, TeamsChanged: tt.teamsChanged,
				StatusChanged: tt.statusChanged, OldStatus: tt.oldStatus,
			}, tt.status)
			if ok != tt.wantOK || (ok && kind != tt.wantKind) {
				t.Fatalf("classifyMatchUpdate() = (%v, %v), want (%v, %v)", kind, ok, tt.wantKind, tt.wantOK)
			}
		})
	}
}
