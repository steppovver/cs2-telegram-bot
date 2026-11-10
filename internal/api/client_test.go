package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchMatchByID(t *testing.T) {
	const valid = `{"id":1789260,"status":"not_started","begin_at":"2026-11-24T09:00:00Z","opponents":[{"opponent":{"id":3213,"name":"Liquid"}},{"opponent":{"id":139562,"name":"GOSH"}}],"tournament":{"name":"Stage 1"}}`
	for _, tt := range []struct {
		name         string
		status       int
		body         string
		transportErr error
		wantNil      bool
		wantErr      bool
	}{
		{name: "found", status: 200, body: valid},
		{name: "canceled", status: 200, body: `{"id":1789260,"status":"canceled"}`, wantNil: true},
		{name: "not found", status: 404, wantNil: true},
		{name: "rate limit", status: 429, wantErr: true},
		{name: "server error", status: 500, wantErr: true},
		{name: "unauthorized", status: 401, wantErr: true},
		{name: "timeout", transportErr: context.DeadlineExceeded, wantErr: true},
		{name: "invalid JSON", status: 200, body: `{`, wantErr: true},
		{name: "null", status: 200, body: `null`, wantErr: true},
		{name: "wrong ID", status: 200, body: strings.Replace(valid, "1789260", "1789261", 1), wantErr: true},
		{name: "unknown time", status: 200, body: strings.Replace(valid, `"2026-11-24T09:00:00Z"`, "null", 1), wantErr: true},
		{name: "unknown opponent", status: 200, body: strings.Replace(valid, `"id":139562`, `"id":0`, 1), wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient("test-token", 1000)
			c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.String() != "https://api.pandascore.co/matches/1789260" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatal("неверный запрос матча по ID")
				}
				if tt.transportErr != nil {
					return nil, tt.transportErr
				}
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body)),
					Header: http.Header{"X-Rate-Limit-Remaining": []string{"899"}}}, nil
			})
			m, err := c.FetchMatchByID(context.Background(), 1789260)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if (m == nil) != tt.wantNil {
					t.Fatalf("match = %+v, wantNil %v", m, tt.wantNil)
				}
				if m != nil && (m.TeamBID != 139562 || m.StageName != "Stage 1") {
					t.Fatalf("потерялись актуальные данные: %+v", m)
				}
			}
			if tt.transportErr == nil {
				if used, remaining, _ := c.APIUsage(); used != 1 || remaining != 899 {
					t.Errorf("учёт запроса: used=%d remaining=%d", used, remaining)
				}
			}
		})
	}
}
