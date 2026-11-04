package bot

import (
	"testing"
	"time"
)

func TestNextSubClick(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute)
	old := now.Add(-3 * time.Minute)
	boundary := now.Add(-subLevelGrace)
	justUnder := now.Add(-subLevelGrace + time.Second)

	tests := []struct {
		name       string
		subscribed bool
		favorite   bool
		at         time.Time
		want       subClick
	}{
		{name: "none", want: subClickInterest},
		{name: "fresh interesting", subscribed: true, at: fresh, want: subClickFavorite},
		{name: "old interesting", subscribed: true, at: old, want: subClickRemove},
		{name: "zero interesting", subscribed: true, want: subClickRemove},
		{name: "boundary", subscribed: true, at: boundary, want: subClickRemove},
		{name: "just under", subscribed: true, at: justUnder, want: subClickFavorite},
		{name: "favorite fresh", subscribed: true, favorite: true, at: fresh, want: subClickRemove},
		{name: "favorite old", subscribed: true, favorite: true, at: old, want: subClickRemove},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextSubClick(tt.subscribed, tt.favorite, tt.at, now)
			if got != tt.want {
				t.Fatalf("nextSubClick() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTeamButtonLabel(t *testing.T) {
	if got := teamButtonLabel("NAVI", false, false); got != "NAVI" {
		t.Fatalf("unsubscribed label = %q", got)
	}
	if got := teamButtonLabel("NAVI", true, false); got != "✅ NAVI" {
		t.Fatalf("interesting label = %q", got)
	}
	if got := teamButtonLabel("NAVI", true, true); got != "⭐ NAVI" {
		t.Fatalf("favorite label = %q", got)
	}
}
