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
	got := buildCombinedUpdateMessage([]matchEvent{ev}, 3)
	if !strings.Contains(got, "<u><b>G2</b></u>") {
		t.Fatalf("нет подчёркивания у любимой: %q", got)
	}
	if !strings.Contains(got, "<b>NAVI</b>") || strings.Contains(got, "<u><b>NAVI</b></u>") {
		t.Fatalf("чужая сторона жирная и без подчёркивания: %q", got)
	}
}

func TestBuildCombinedUpdateMessageShowsCurrentMatchDetails(t *testing.T) {
	const matchDetails = "🛡 <b>Spirit</b> vs <b>G2</b>\n"
	const currentTime = `<tg-time unix="1794480600" format="dt">13:50 12.11 UTC+3</tg-time>`
	tests := []struct {
		name        string
		kind        matchEventKind
		timeChanged bool
		heading     string
		timeLabel   string
	}{
		{"rescheduled", eventTimeChanged, true, "⚠️ <b>Время матчей изменено!</b>", "Новое время"},
		{"opponent determined or replaced", eventOpponent, false, "🔄 <b>Определились соперники!</b>", "Время"},
		{"opponent and time changed", eventOpponent, true, "🔄 <b>Определились соперники!</b>", "Новое время"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := matchEvent{
				kind:        tt.kind,
				timeChanged: tt.timeChanged,
				match: domain.Match{
					TeamA: "Spirit",
					TeamB: "G2",
					Time:  time.Date(2026, 11, 12, 10, 50, 0, 0, time.UTC),
				},
			}
			want := tt.heading + "\n\n" + matchDetails + "⏰ " + tt.timeLabel + ": " + currentTime + "\n"
			if got := buildCombinedUpdateMessage([]matchEvent{ev}, 3); got != want {
				t.Fatalf("buildCombinedUpdateMessage() = %q, want %q", got, want)
			}
		})
	}
}
