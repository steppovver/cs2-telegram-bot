package youtube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu      sync.Mutex
	todo    []string
	saved   map[string]ChannelInfo
	cleaned bool
}

func (m *memStore) GetYouTubeVideosForResolve(from, to, retryBefore time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.todo...)
	return out, nil
}

func (m *memStore) SaveStreamChannel(videoID, channel, channelURL string, checkedAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saved == nil {
		m.saved = make(map[string]ChannelInfo)
	}
	m.saved[videoID] = ChannelInfo{Name: channel, URL: channelURL}
	return nil
}

func (m *memStore) CleanOldStreamChannels(before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleaned = true
	return nil
}

func TestResolverRunCycleSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"author_name":"ESL","author_url":"https://www.youtube.com/@ESL"}`))
	}))
	defer srv.Close()

	st := &memStore{todo: []string{"vidAAAA1111"}}
	c := NewClient()
	c.endpoint = srv.URL
	r := NewResolver(st, c)

	if blocked := r.runCycle(context.Background(), time.Now(), 0); blocked {
		t.Fatal("unexpected backoff")
	}
	if !st.cleaned {
		t.Error("expected cleanup call")
	}
	got := st.saved["vidAAAA1111"]
	if got.Name != "ESL" {
		t.Errorf("saved = %+v", got)
	}
}

func TestResolverRunCycleNegativeCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	st := &memStore{todo: []string{"vidBBBB2222"}}
	c := NewClient()
	c.endpoint = srv.URL
	r := NewResolver(st, c)

	if blocked := r.runCycle(context.Background(), time.Now(), 0); blocked {
		t.Fatal("unexpected backoff")
	}
	got, ok := st.saved["vidBBBB2222"]
	if !ok || got.Name != "" {
		t.Errorf("expected negative cache, got ok=%v %+v", ok, got)
	}
}

func TestResolverRunCycleBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	st := &memStore{todo: []string{"a1111111111", "b2222222222", "c3333333333"}}
	c := NewClient()
	c.endpoint = srv.URL
	r := NewResolver(st, c)

	if blocked := r.runCycle(context.Background(), time.Now(), 0); !blocked {
		t.Fatal("expected backoff after consecutive errors")
	}
	if len(st.saved) != 0 {
		t.Errorf("network errors must not write cache, got %v", st.saved)
	}
}

func TestResolverRunCycleContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"author_name":"X","author_url":""}`))
	}))
	defer srv.Close()

	st := &memStore{todo: []string{"a1111111111", "b2222222222"}}
	c := NewClient()
	c.endpoint = srv.URL
	r := NewResolver(st, c)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if blocked := r.runCycle(ctx, time.Now(), time.Hour); blocked {
		t.Fatal("cancelled cycle should not backoff")
	}
}

func TestErrNotEmbeddableIs(t *testing.T) {
	err := errors.Join(ErrNotEmbeddable)
	if !errors.Is(err, ErrNotEmbeddable) {
		t.Fatal("errors.Is failed")
	}
}
