package youtube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientResolve(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("format=%q", r.URL.Query().Get("format"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"author_name":"NAVI","author_url":"https://www.youtube.com/@NAVI","title":"live"}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.endpoint = srv.URL
	info, err := c.Resolve(context.Background(), "Im3OHRzxkeg")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if info.Name != "NAVI" || info.URL != "https://www.youtube.com/@NAVI" {
		t.Errorf("info = %+v", info)
	}
}

func TestClientResolveNotEmbeddable(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		c := NewClient()
		c.endpoint = srv.URL
		_, err := c.Resolve(context.Background(), "abc123XYZ_1")
		srv.Close()
		if !errors.Is(err, ErrNotEmbeddable) {
			t.Errorf("status %d: err=%v, want ErrNotEmbeddable", code, err)
		}
	}
}

func TestClientResolveServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient()
	c.endpoint = srv.URL
	_, err := c.Resolve(context.Background(), "abc123XYZ_1")
	if err == nil || errors.Is(err, ErrNotEmbeddable) {
		t.Fatalf("err=%v, want ordinary error", err)
	}
}

func TestClientResolveEmptyAuthor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"author_name":"","title":"x"}`))
	}))
	defer srv.Close()

	c := NewClient()
	c.endpoint = srv.URL
	_, err := c.Resolve(context.Background(), "abc123XYZ_1")
	if !errors.Is(err, ErrNotEmbeddable) {
		t.Fatalf("err=%v, want ErrNotEmbeddable", err)
	}
}
