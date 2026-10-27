package config

import (
	"strings"
	"testing"
)

func TestNormalizeTelegramMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{name: "empty", in: "", want: TelegramModePolling, wantOK: true},
		{name: "spaces", in: "  ", want: TelegramModePolling, wantOK: true},
		{name: "polling", in: "polling", want: TelegramModePolling, wantOK: true},
		{name: "webhook upper", in: "WEBHOOK", want: TelegramModeWebhook, wantOK: true},
		{name: "webhook padded", in: "  webhook  ", want: TelegramModeWebhook, wantOK: true},
		{name: "unknown", in: "push", want: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := NormalizeTelegramMode(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("mode=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateWebhook(t *testing.T) {
	t.Parallel()

	valid := &Config{
		TelegramMode:     TelegramModeWebhook,
		WebhookPublicURL: "https://bot.example.com/tg",
		WebhookSecret:    "abcDEF0123_-",
		WebhookListen:    DefaultWebhookListen,
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "polling skips checks",
			mutate: func(c *Config) {
				c.TelegramMode = TelegramModePolling
				c.WebhookPublicURL = ""
				c.WebhookSecret = ""
			},
		},
		{
			name: "empty mode is polling",
			mutate: func(c *Config) {
				c.TelegramMode = ""
				c.WebhookPublicURL = ""
				c.WebhookSecret = ""
			},
		},
		{
			name:   "valid webhook",
			mutate: func(*Config) {},
		},
		{
			name: "unknown mode",
			mutate: func(c *Config) {
				c.TelegramMode = "push"
			},
			wantErr: "unknown telegram_mode",
		},
		{
			name: "empty url",
			mutate: func(c *Config) {
				c.WebhookPublicURL = ""
			},
			wantErr: "webhook_public_url обязателен",
		},
		{
			name: "http url",
			mutate: func(c *Config) {
				c.WebhookPublicURL = "http://bot.example.com/tg"
			},
			wantErr: "https://",
		},
		{
			name: "url without host",
			mutate: func(c *Config) {
				c.WebhookPublicURL = "https:///tg"
			},
			wantErr: "без хоста",
		},
		{
			name: "empty secret",
			mutate: func(c *Config) {
				c.WebhookSecret = ""
			},
			wantErr: "webhook_secret обязателен",
		},
		{
			name: "secret too long",
			mutate: func(c *Config) {
				c.WebhookSecret = strings.Repeat("a", 257)
			},
			wantErr: "длиннее 256",
		},
		{
			name: "secret bad char",
			mutate: func(c *Config) {
				c.WebhookSecret = "abc/def"
			},
			wantErr: "недопустимый символ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := *valid
			tt.mutate(&cfg)
			err := ValidateWebhook(&cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNormalizeWebhookListen(t *testing.T) {
	t.Parallel()

	if got := NormalizeWebhookListen(""); got != DefaultWebhookListen {
		t.Fatalf("empty listen=%q, want %q", got, DefaultWebhookListen)
	}
	if got := NormalizeWebhookListen(" 127.0.0.1:9999 "); got != "127.0.0.1:9999" {
		t.Fatalf("custom listen=%q", got)
	}
}
