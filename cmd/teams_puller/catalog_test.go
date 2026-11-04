package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

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
