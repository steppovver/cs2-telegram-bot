package bot

import (
	"strings"
	"testing"
	"time"
)

func TestAPIUsageLine(t *testing.T) {
	tests := []struct {
		name                   string
		used, remaining, limit int
		pace                   pollPaceSnapshot
		wantContains           []string
		wantNotContains        []string
	}{
		{
			name: "остаток н/д без режима",
			used: 3, remaining: -1, limit: 1000,
			wantContains:    []string{"остаток н/д", "лимит 1000"},
			wantNotContains: []string{"Опрос:"},
		},
		{
			name: "остаток есть, поллер ещё не считал",
			used: 10, remaining: 900, limit: 1000,
			wantContains:    []string{"остаток 900/1000", "90%"},
			wantNotContains: []string{"Опрос:"},
		},
		{
			name: "темп 50%",
			used: 10, remaining: 900, limit: 1000,
			pace:         pollPaceSnapshot{known: true, delay: time.Hour / 500, mode: paceDynamic},
			wantContains: []string{"Опрос: темп 7s", "50% лимита"},
		},
		{
			name: "гистерезис после падения ≤ 40%",
			used: 50, remaining: 400, limit: 1000,
			pace:         pollPaceSnapshot{known: true, delay: 60 * time.Second, mode: paceFallback, restricted: true},
			wantContains: []string{"фиксированный интервал 1m0s", "ждём остаток > 50%"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := apiUsageLine(tt.used, tt.remaining, tt.limit, tt.pace)
			for _, s := range tt.wantContains {
				if !strings.Contains(got, s) {
					t.Fatalf("apiUsageLine() = %q, want contains %q", got, s)
				}
			}
			for _, s := range tt.wantNotContains {
				if strings.Contains(got, s) {
					t.Fatalf("apiUsageLine() = %q, want not contain %q", got, s)
				}
			}
		})
	}
}
