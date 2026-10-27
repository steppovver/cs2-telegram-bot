package bot

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cs2bot/internal/config"

	"gopkg.in/telebot.v3"
)

const testWebhookSecret = "test_secret-123"

// fakeTelegramAPI отвечает ok на любой метод и считает вызовы setWebhook.
func fakeTelegramAPI(t *testing.T, failFirst int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/setWebhook") {
			if n := calls.Add(1); n <= failFirst {
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: test"}`)
				return
			}
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func newTestTelebot(t *testing.T, apiURL string, poller telebot.Poller) *telebot.Bot {
	t.Helper()
	tb, err := telebot.NewBot(telebot.Settings{
		URL:     apiURL,
		Token:   "123:test-token",
		Offline: true,
		Poller:  poller,
	})
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	return tb
}

func newTestWebhookPoller(t *testing.T) *WebhookPoller {
	t.Helper()
	p, err := NewWebhookPoller("127.0.0.1:0", "https://bot.example.com/tg", testWebhookSecret)
	if err != nil {
		t.Fatalf("NewWebhookPoller: %v", err)
	}
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func postUpdate(t *testing.T, addr, secret, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/tg", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if secret != "" {
		req.Header.Set(webhookSecretHeader, secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestWebhookPollerHandlesRequests(t *testing.T) {
	api, calls := fakeTelegramAPI(t, 0)
	p := newTestWebhookPoller(t)
	tb := newTestTelebot(t, api.URL, p)

	dest := make(chan telebot.Update, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		p.Poll(tb, dest, stop)
		close(done)
	}()
	waitFor(t, "setWebhook", func() bool { return calls.Load() == 1 })

	if code := postUpdate(t, p.Addr(), "", `{"update_id":1}`); code != http.StatusUnauthorized {
		t.Fatalf("no secret: status %d, want 401", code)
	}
	if code := postUpdate(t, p.Addr(), "wrong", `{"update_id":1}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: status %d, want 401", code)
	}
	if code := postUpdate(t, p.Addr(), testWebhookSecret, `{not json`); code != http.StatusBadRequest {
		t.Fatalf("bad json: status %d, want 400", code)
	}

	resp, err := http.Get("http://" + p.Addr() + "/tg")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", resp.StatusCode)
	}

	if code := postUpdate(t, p.Addr(), testWebhookSecret, `{"update_id":42}`); code != http.StatusOK {
		t.Fatalf("valid update: status %d, want 200", code)
	}
	select {
	case upd := <-dest:
		if upd.ID != 42 {
			t.Fatalf("update id %d, want 42", upd.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("update was not delivered to dest")
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Poll did not return after stop")
	}
}

// Регрессия: telebot.Webhook паникует на Bot.Stop (повторный close(stop)).
func TestWebhookPollerBotStopNoPanic(t *testing.T) {
	api, calls := fakeTelegramAPI(t, 0)
	p := newTestWebhookPoller(t)
	tb := newTestTelebot(t, api.URL, p)

	started := make(chan struct{})
	go func() {
		close(started)
		tb.Start()
	}()
	<-started
	waitFor(t, "setWebhook", func() bool { return calls.Load() == 1 })

	stopped := make(chan struct{})
	go func() {
		tb.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Bot.Stop did not return")
	}

	if _, err := net.Dial("tcp", p.Addr()); err == nil {
		t.Fatal("webhook HTTP server still accepts connections after stop")
	}
}

func TestWebhookPollerStopDuringRegisterRetry(t *testing.T) {
	api, calls := fakeTelegramAPI(t, 1000)
	p := newTestWebhookPoller(t)
	tb := newTestTelebot(t, api.URL, p)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		p.Poll(tb, make(chan telebot.Update, 1), stop)
		close(done)
	}()
	waitFor(t, "first setWebhook attempt", func() bool { return calls.Load() >= 1 })

	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Poll did not return while waiting for register retry")
	}
}

func TestNewWebhookPollerPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if _, err := NewWebhookPoller(ln.Addr().String(), "https://bot.example.com/tg", testWebhookSecret); err == nil {
		t.Fatal("expected error for busy port")
	}
}

func TestNewTelegramPoller(t *testing.T) {
	p, err := NewTelegramPoller(&config.Config{})
	if err != nil {
		t.Fatalf("polling: %v", err)
	}
	if _, ok := p.(*telebot.LongPoller); !ok {
		t.Fatalf("polling: got %T, want *telebot.LongPoller", p)
	}

	if _, err := NewTelegramPoller(&config.Config{TelegramMode: "push"}); err == nil {
		t.Fatal("unknown mode: expected error")
	}
	if _, err := NewTelegramPoller(&config.Config{TelegramMode: "webhook"}); err == nil {
		t.Fatal("webhook without url/secret: expected error")
	}

	p, err = NewTelegramPoller(&config.Config{
		TelegramMode:     "webhook",
		WebhookPublicURL: "https://bot.example.com/tg",
		WebhookListen:    "127.0.0.1:0",
		WebhookSecret:    testWebhookSecret,
	})
	if err != nil {
		t.Fatalf("webhook: %v", err)
	}
	wp, ok := p.(*WebhookPoller)
	if !ok {
		t.Fatalf("webhook: got %T, want *WebhookPoller", p)
	}
	wp.listener.Close()
}

func TestRedactToken(t *testing.T) {
	err := errors.New(`Post "https://api.telegram.org/bot123:abc/setWebhook": dial tcp: timeout`)
	got := redactToken(err, "123:abc")
	if strings.Contains(got, "123:abc") {
		t.Fatalf("token not redacted: %s", got)
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("missing marker: %s", got)
	}
	if redactToken(nil, "x") != "" {
		t.Fatal("nil error should give empty string")
	}
}
