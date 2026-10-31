package bot

import (
	"time"

	"cs2bot/internal/config"
)

const (
	// apiPaceTargetPercent — целевой расход лимита в час при динамическом темпе
	// и порог возврата: динамика снова включается, только когда остаток выше этой доли.
	apiPaceTargetPercent = 50
	// apiPaceHeadroomPercent — порог выключения динамики: на этой доле остатка и ниже
	// переходим на фиксированный интервал.
	apiPaceHeadroomPercent = 40
)

type pollPaceMode int

const (
	// paceFallback — poll_interval_seconds: нет заголовка, нет запросов или гистерезис запаса.
	paceFallback pollPaceMode = iota
	// paceDynamic — ровный темп apiPaceTargetPercent лимита в час.
	paceDynamic
)

// pollPace — пауза до следующего цикла живого поллера и почему она такая.
type pollPace struct {
	Delay      time.Duration
	Mode       pollPaceMode
	Restricted bool // true: выключили на ≤ headroom, ждём остаток > target
}

// nextPollDelay считает паузу после цикла опроса.
// remaining — последний X-Rate-Limit-Remaining (-1 = ещё не видели).
// cycleReqs — HTTP-ответы только что завершённого цикла (0 = в API не ходили).
// fallback — poll_interval_seconds.
// wasRestricted — прошлый цикл был в зоне гистерезиса (после падения ≤ headroom).
func nextPollDelay(remaining, limit, cycleReqs int, fallback time.Duration, wasRestricted bool) pollPace {
	if fallback <= 0 {
		fallback = time.Duration(config.DefaultPollIntervalSec) * time.Second
	}
	restricted := paceRestricted(remaining, limit, wasRestricted)
	if remaining < 0 || limit <= 0 || cycleReqs <= 0 {
		return pollPace{Delay: fallback, Mode: paceFallback, Restricted: restricted}
	}
	if restricted {
		return pollPace{Delay: fallback, Mode: paceFallback, Restricted: true}
	}
	budget := limit * apiPaceTargetPercent / 100
	if budget <= 0 {
		return pollPace{Delay: fallback, Mode: paceFallback, Restricted: false}
	}
	delay := time.Hour * time.Duration(cycleReqs) / time.Duration(budget)
	if delay < time.Second {
		delay = time.Second
	}
	return pollPace{Delay: delay, Mode: paceDynamic, Restricted: false}
}

// paceRestricted — гистерезис запаса лимита:
// включается при remaining ≤ headroom%, снимается только при remaining > target%.
func paceRestricted(remaining, limit int, wasRestricted bool) bool {
	if remaining < 0 || limit <= 0 {
		return wasRestricted
	}
	low := limit * apiPaceHeadroomPercent / 100
	high := limit * apiPaceTargetPercent / 100
	if remaining <= low {
		return true
	}
	if wasRestricted && remaining <= high {
		return true
	}
	return false
}

// shouldNotifyHeadroom решает, слать ли админам сообщение о выключении динамики.
// Уведомление только на переходе в restricted; пока остаёмся в нём — молчим.
// Снимается после remaining > target%. Дребезг режет throttle: не чаще раза в час.
func shouldNotifyHeadroom(wasRestricted, nowRestricted bool, lastWarn, now time.Time) bool {
	if !nowRestricted || wasRestricted {
		return false
	}
	if !lastWarn.IsZero() && now.Sub(lastWarn) < time.Hour {
		return false
	}
	return true
}
