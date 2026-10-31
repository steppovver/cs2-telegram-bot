package bot

import (
	"testing"
	"time"

	"cs2bot/internal/config"
)

func TestNextPollDelay(t *testing.T) {
	fallback := 60 * time.Second
	tests := []struct {
		name                   string
		remaining, limit, reqs int
		fallback               time.Duration
		wasRestricted          bool
		wantDelay              time.Duration
		wantMode               pollPaceMode
		wantRestricted         bool
	}{
		{
			name:      "темп 50% один запрос",
			remaining: 900, limit: 1000, reqs: 1,
			fallback:  fallback,
			wantDelay: time.Hour / 500,
			wantMode:  paceDynamic,
		},
		{
			name:      "темп 50% четыре запроса",
			remaining: 900, limit: 1000, reqs: 4,
			fallback:  fallback,
			wantDelay: 4 * time.Hour / 500,
			wantMode:  paceDynamic,
		},
		{
			name:      "остаток ровно 40% — выключение",
			remaining: 400, limit: 1000, reqs: 1,
			fallback:        fallback,
			wantDelay:      fallback,
			wantMode:       paceFallback,
			wantRestricted: true,
		},
		{
			name:      "остаток ниже 40%",
			remaining: 100, limit: 1000, reqs: 1,
			fallback:        fallback,
			wantDelay:      fallback,
			wantMode:       paceFallback,
			wantRestricted: true,
		},
		{
			name:      "после выключения остаток 45% — ещё ждём",
			remaining: 450, limit: 1000, reqs: 1,
			fallback:        fallback,
			wasRestricted:  true,
			wantDelay:      fallback,
			wantMode:       paceFallback,
			wantRestricted: true,
		},
		{
			name:      "после выключения остаток ровно 50% — ещё ждём",
			remaining: 500, limit: 1000, reqs: 1,
			fallback:        fallback,
			wasRestricted:  true,
			wantDelay:      fallback,
			wantMode:       paceFallback,
			wantRestricted: true,
		},
		{
			name:      "после выключения остаток 51% — снова динамика",
			remaining: 501, limit: 1000, reqs: 1,
			fallback:       fallback,
			wasRestricted: true,
			wantDelay:     time.Hour / 500,
			wantMode:      paceDynamic,
		},
		{
			name:      "без restricted остаток 45% — динамика",
			remaining: 450, limit: 1000, reqs: 1,
			fallback:  fallback,
			wantDelay: time.Hour / 500,
			wantMode:  paceDynamic,
		},
		{
			name:      "нет заголовка",
			remaining: -1, limit: 1000, reqs: 1,
			fallback:  fallback,
			wantDelay: fallback,
			wantMode:  paceFallback,
		},
		{
			name:      "нет заголовка сохраняет restricted",
			remaining: -1, limit: 1000, reqs: 1,
			fallback:        fallback,
			wasRestricted:  true,
			wantDelay:      fallback,
			wantMode:       paceFallback,
			wantRestricted: true,
		},
		{
			name:      "нулевой цикл",
			remaining: 900, limit: 1000, reqs: 0,
			fallback:  fallback,
			wantDelay: fallback,
			wantMode:  paceFallback,
		},
		{
			name:      "пол 1с при огромном лимите",
			remaining: 999999, limit: 1000000, reqs: 1,
			fallback:  fallback,
			wantDelay: time.Second,
			wantMode:  paceDynamic,
		},
		{
			name:      "fallback 0 = дефолт конфига",
			remaining: -1, limit: 1000, reqs: 0,
			fallback:  0,
			wantDelay: time.Duration(config.DefaultPollIntervalSec) * time.Second,
			wantMode:  paceFallback,
		},
		{
			name:      "остаток чуть выше 40% без restricted",
			remaining: 401, limit: 1000, reqs: 1,
			fallback:  fallback,
			wantDelay: time.Hour / 500,
			wantMode:  paceDynamic,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextPollDelay(tt.remaining, tt.limit, tt.reqs, tt.fallback, tt.wasRestricted)
			if got.Delay != tt.wantDelay || got.Mode != tt.wantMode || got.Restricted != tt.wantRestricted {
				t.Fatalf("nextPollDelay() = %+v, want delay=%v mode=%v restricted=%v",
					got, tt.wantDelay, tt.wantMode, tt.wantRestricted)
			}
		})
	}
}

func TestPaceRestricted(t *testing.T) {
	tests := []struct {
		name          string
		remaining     int
		limit         int
		wasRestricted bool
		want          bool
	}{
		{name: "падение на 40%", remaining: 400, limit: 1000, want: true},
		{name: "зона гистерезиса", remaining: 450, limit: 1000, wasRestricted: true, want: true},
		{name: "ровно target%", remaining: 500, limit: 1000, wasRestricted: true, want: true},
		{name: "выше target%", remaining: 501, limit: 1000, wasRestricted: true, want: false},
		{name: "выше headroom без флага", remaining: 450, limit: 1000, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paceRestricted(tt.remaining, tt.limit, tt.wasRestricted); got != tt.want {
				t.Fatalf("paceRestricted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldNotifyHeadroom(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	hourAgo := now.Add(-time.Hour)
	recent := now.Add(-time.Minute)

	tests := []struct {
		name                         string
		wasRestricted, nowRestricted bool
		lastWarn                     time.Time
		want                         bool
	}{
		{name: "переход в restricted", wasRestricted: false, nowRestricted: true, want: true},
		{name: "уже restricted", wasRestricted: true, nowRestricted: true, want: false},
		{name: "динамика", wasRestricted: false, nowRestricted: false, want: false},
		{name: "выход из restricted", wasRestricted: true, nowRestricted: false, want: false},
		{name: "повтор после восстановления", wasRestricted: false, nowRestricted: true, lastWarn: hourAgo, want: true},
		{name: "дребезг в течение часа", wasRestricted: false, nowRestricted: true, lastWarn: recent, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldNotifyHeadroom(tt.wasRestricted, tt.nowRestricted, tt.lastWarn, now)
			if got != tt.want {
				t.Fatalf("shouldNotifyHeadroom() = %v, want %v", got, tt.want)
			}
		})
	}
}
