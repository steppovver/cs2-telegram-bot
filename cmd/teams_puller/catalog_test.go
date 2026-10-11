package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"cs2bot/internal/storage"
)

func openPullerDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "teams.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.InitSchema(db); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return db
}

func TestApplyCatalogReplacesRosterAndDropsGoneTeams(t *testing.T) {
	db := openPullerDB(t)
	if _, err := db.Exec(`INSERT INTO users (id) VALUES (1)`); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO teams (id, name) VALUES (10, 'Gone'), (20, 'Stay'), (30, 'Empty')`); err != nil {
		t.Fatalf("teams: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO players (id, team_id, name) VALUES (101, 10, 'old-gone'), (201, 20, 'left'), (301, 30, 'was-here')`); err != nil {
		t.Fatalf("players: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO subscriptions (user_id, team_id) VALUES (1, 10), (1, 20), (1, 30)`); err != nil {
		t.Fatalf("subs: %v", err)
	}

	err := applyCatalog(context.Background(), db, []pandaScoreTeam{
		{ID: 20, Name: "Stay", Players: []pandaScorePlayer{{ID: 202, Name: "new"}}},
		{ID: 30, Name: "Empty"},
		{ID: 40, Name: "NoRoster"},
	})
	if err != nil {
		t.Fatalf("applyCatalog: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams WHERE id = 10`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("пропавшая команда осталась в teams")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE team_id = 10`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("подписка на пропавшую команду осталась")
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM teams WHERE id = 20`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Stay" {
		t.Fatalf("имя команды = %q", name)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM players WHERE team_id = 20 AND id = 201`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("ушедший игрок остался в составе")
	}
	if err := db.QueryRow(`SELECT name FROM players WHERE id = 202`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "new" {
		t.Fatalf("новый игрок = %q", name)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE team_id = 20`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("подписка на живую команду = %d", n)
	}

	if err := db.QueryRow(`SELECT COUNT(*) FROM teams WHERE id = 30`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("команда без состава удалена")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM players WHERE team_id = 30`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("старый состав команды без игроков остался")
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM teams WHERE id = 40`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("новая команда без состава попала в каталог")
	}
}

func TestApplyCatalogEmptyDoesNotWrite(t *testing.T) {
	db := openPullerDB(t)
	if _, err := db.Exec(`INSERT INTO teams (id, name) VALUES (1, 'Keep')`); err != nil {
		t.Fatal(err)
	}
	if err := applyCatalog(context.Background(), db, nil); err == nil {
		t.Fatal("пустой каталог должен быть ошибкой")
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM teams WHERE id = 1`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "Keep" {
		t.Fatalf("имя = %q", name)
	}
}

func TestGetJSONRetriesServerError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"name":"A","players":[{"id":2,"name":"p"}]}]`))
	}))
	defer srv.Close()

	var got []pandaScoreTeam
	if err := getJSON(context.Background(), srv.Client(), srv.URL, "token", &got); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("запросов = %d, want 2", hits.Load())
	}
	if len(got) != 1 || got[0].Name != "A" || len(got[0].Players) != 1 {
		t.Fatalf("ответ = %+v", got)
	}
}

func TestGetJSONClientErrorNoRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	var got []pandaScoreTeam
	if err := getJSON(context.Background(), srv.Client(), srv.URL, "token", &got); err == nil {
		t.Fatal("ожидалась ошибка 400")
	}
	if hits.Load() != 1 {
		t.Fatalf("запросов = %d, want 1", hits.Load())
	}
}
