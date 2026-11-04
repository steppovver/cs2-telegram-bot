package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateOldSubscriptionIsInteresting(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id) VALUES (1)`); err != nil {
		t.Fatalf("users: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO teams (id, name) VALUES (10, 'NAVI')`); err != nil {
		t.Fatalf("teams: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO subscriptions (user_id, team_id) VALUES (1, 10)`); err != nil {
		t.Fatalf("subscriptions: %v", err)
	}

	if err := migrateSchema(db); err != nil {
		t.Fatalf("migrateSchema: %v", err)
	}
	var fav int
	var at int64
	if err := db.QueryRow(`SELECT favorite, subscribed_at FROM subscriptions WHERE user_id = 1 AND team_id = 10`).Scan(&fav, &at); err != nil {
		t.Fatalf("select: %v", err)
	}
	if fav != 0 || at != 0 {
		t.Fatalf("favorite=%d subscribed_at=%d, want 0, 0", fav, at)
	}
}

func TestSubscriptionLevelUpsertAndFans(t *testing.T) {
	s := openTestStorage(t)
	if err := s.Subscribe(1, 10, "A", false); err != nil {
		t.Fatalf("interesting A: %v", err)
	}
	if err := s.Subscribe(1, 20, "B", true); err != nil {
		t.Fatalf("favorite B: %v", err)
	}
	if err := s.Subscribe(2, 10, "A", true); err != nil {
		t.Fatalf("favorite A: %v", err)
	}
	if err := s.Subscribe(3, 20, "B", false); err != nil {
		t.Fatalf("interesting B: %v", err)
	}

	subs, err := s.GetUserSubscriptions(1)
	if err != nil {
		t.Fatalf("GetUserSubscriptions: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("subs = %d, want 2", len(subs))
	}
	for _, sub := range subs {
		if sub.SubscribedAt.IsZero() || time.Since(sub.SubscribedAt) > time.Minute {
			t.Errorf("team %d subscribed_at = %v, want recent", sub.ID, sub.SubscribedAt)
		}
		switch sub.ID {
		case 10:
			if sub.Favorite {
				t.Error("team A should be interesting")
			}
		case 20:
			if !sub.Favorite {
				t.Error("team B should be favorite")
			}
		default:
			t.Errorf("unexpected team %d", sub.ID)
		}
	}

	if err := s.Subscribe(1, 10, "A", true); err != nil {
		t.Fatalf("promote A: %v", err)
	}
	subs, err = s.GetUserSubscriptions(1)
	if err != nil {
		t.Fatalf("GetUserSubscriptions after promote: %v", err)
	}
	for _, sub := range subs {
		if sub.ID == 10 && !sub.Favorite {
			t.Fatal("promote did not set favorite")
		}
	}

	fans, err := s.GetMatchFans(10, 20)
	if err != nil {
		t.Fatalf("GetMatchFans: %v", err)
	}
	got := map[int64][2]bool{}
	for _, f := range fans {
		got[f.UserID] = [2]bool{f.FavoriteA, f.FavoriteB}
	}
	want := map[int64][2]bool{
		1: {true, true},
		2: {true, false},
		3: {false, false},
	}
	if len(got) != len(want) {
		t.Fatalf("fans = %v, want %v", got, want)
	}
	for id, flags := range want {
		if got[id] != flags {
			t.Errorf("user %d flags = %v, want %v", id, got[id], flags)
		}
	}
}
