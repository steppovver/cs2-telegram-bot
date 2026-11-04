package bot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cs2bot/internal/config"

	"gopkg.in/telebot.v3"
)

const (
	webhookSecretHeader    = "X-Telegram-Bot-Api-Secret-Token"
	webhookMaxBodyBytes    = 1 << 20
	webhookRetryDelay      = 30 * time.Second
	webhookShutdownTimeout = 5 * time.Second
)

// NewTelegramPoller выбирает способ получения апдейтов по telegram_mode.
// Невалидный режим или конфиг вебхука — ошибка, без отката на polling.
func NewTelegramPoller(cfg *config.Config) (telebot.Poller, error) {
	if err := config.ValidateWebhook(cfg); err != nil {
		return nil, err
	}
	mode, _ := config.NormalizeTelegramMode(cfg.TelegramMode)
	if mode != config.TelegramModeWebhook {
		return &retryPoller{timeout: 10 * time.Second}, nil
	}
	return NewWebhookPoller(cfg.WebhookListen, cfg.WebhookPublicURL, cfg.WebhookSecret)
}

// PrepareTelegramMode в режиме polling снимает зарегистрированный webhook
// (иначе getUpdates вернет 409) и пишет режим в лог без секретов.
func PrepareTelegramMode(tb *telebot.Bot, cfg *config.Config) {
	mode, _ := config.NormalizeTelegramMode(cfg.TelegramMode)
	if mode == config.TelegramModeWebhook {
		slog.Info("Режим получения апдейтов Telegram",
			slog.String("mode", mode),
			slog.String("public_url", cfg.WebhookPublicURL),
			slog.String("listen", cfg.WebhookListen),
		)
		return
	}
	if err := tb.RemoveWebhook(false); err != nil {
		slog.Warn("Не удалось снять webhook перед polling", slog.String("error", redactToken(err, tb.Token)))
	}
	slog.Info("Режим получения апдейтов Telegram", slog.String("mode", mode))
}

// retryPoller — long polling с паузой после ошибки getUpdates и выходом
// по stop, даже если отправка апдейта в канал ещё не прошла.
type retryPoller struct {
	timeout time.Duration
	offset  int
}

func (p *retryPoller) Poll(b *telebot.Bot, dest chan telebot.Update, stop chan struct{}) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		select {
		case <-stop:
			return
		default:
		}

		params := map[string]string{
			"offset":          strconv.Itoa(p.offset),
			"timeout":         strconv.Itoa(int(p.timeout / time.Second)),
			"allowed_updates": "null",
		}
		data, err := b.Raw("getUpdates", params)
		if err != nil {
			slog.Warn("getUpdates", slog.String("error", redactToken(err, b.Token)))
		} else {
			var resp struct {
				Result []telebot.Update `json:"result"`
			}
			if err = json.Unmarshal(data, &resp); err != nil {
				slog.Warn("getUpdates: не разобрали ответ", slog.String("error", redactToken(err, b.Token)))
			} else {
				backoff = time.Second
				for _, upd := range resp.Result {
					if upd.ID >= p.offset {
						p.offset = upd.ID + 1
					}
					select {
					case dest <- upd:
					case <-stop:
						return
					}
				}
				continue
			}
		}

		select {
		case <-stop:
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// WebhookPoller принимает апдейты по HTTP за reverse proxy. Заменяет
// telebot.Webhook: тот паникует на остановке (повторный close(stop))
// и молча игнорирует ошибки HTTP-сервера и setWebhook.
type WebhookPoller struct {
	listener  net.Listener
	publicURL string
	secret    string
	// OnServeExit вызывается, если HTTP-сервер умер сам.
	// Из него нельзя синхронно ждать Poll: Poll уже выходит.
	OnServeExit func(error)
}

// NewWebhookPoller сразу занимает порт, чтобы конфликт был виден на старте.
func NewWebhookPoller(listen, publicURL, secret string) (*WebhookPoller, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("webhook listen %s: %w", listen, err)
	}
	return &WebhookPoller{listener: ln, publicURL: publicURL, secret: secret}, nil
}

// Addr — фактический адрес HTTP-сервера.
func (p *WebhookPoller) Addr() string {
	return p.listener.Addr().String()
}

// Poll реализует telebot.Poller. Канал stop закрывает сам telebot.Bot.
func (p *WebhookPoller) Poll(b *telebot.Bot, dest chan telebot.Update, stop chan struct{}) {
	srv := &http.Server{
		Handler:           p.handler(dest, stop),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(p.listener)
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), webhookShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Warn("Ошибка остановки HTTP-сервера webhook", slog.Any("error", err))
		}
	}()

	if !p.register(b, stop) {
		return
	}

	select {
	case <-stop:
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP-сервер webhook остановился, апдейты не принимаются", slog.Any("error", err))
			if p.OnServeExit != nil {
				p.OnServeExit(err)
			}
		}
	}
}

// register вызывает setWebhook с повтором, пока не получится или не придет stop.
func (p *WebhookPoller) register(b *telebot.Bot, stop <-chan struct{}) bool {
	wh := &telebot.Webhook{
		SecretToken: p.secret,
		Endpoint:    &telebot.WebhookEndpoint{PublicURL: p.publicURL},
	}
	for {
		err := b.SetWebhook(wh)
		if err == nil {
			slog.Info("Webhook зарегистрирован", slog.String("public_url", p.publicURL))
			return true
		}
		slog.Error("Не удалось зарегистрировать webhook, повтор",
			slog.String("error", redactToken(err, b.Token)),
			slog.Duration("retry_in", webhookRetryDelay),
		)
		select {
		case <-stop:
			return false
		case <-time.After(webhookRetryDelay):
		}
	}
}

func (p *WebhookPoller) handler(dest chan<- telebot.Update, stop <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		got := r.Header.Get(webhookSecretHeader)
		if subtle.ConstantTimeCompare([]byte(got), []byte(p.secret)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var upd telebot.Update
		body := http.MaxBytesReader(w, r.Body, webhookMaxBodyBytes)
		if err := json.NewDecoder(body).Decode(&upd); err != nil {
			slog.Warn("Некорректный апдейт webhook", slog.Any("error", err))
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// 503 на остановке: Telegram повторит доставку после рестарта.
		select {
		case dest <- upd:
			w.WriteHeader(http.StatusOK)
		case <-stop:
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
		case <-r.Context().Done():
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	})
}

// redactToken убирает токен бота из текста ошибки: сетевые ошибки
// telebot содержат полный URL запроса вида /bot<token>/method.
func redactToken(err error, token string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, token, "<redacted>")
}

var _ telebot.Poller = (*WebhookPoller)(nil)
