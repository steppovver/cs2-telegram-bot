package bot

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"sort"
	"strings"
	"time"

	"cs2bot/internal/config"
	"cs2bot/internal/domain"
)

// StartPoller опрашивает предстоящие/live матчи. Пауза между циклами
// динамическая (apiPaceTargetPercent лимита в час) с гистерезисом запаса;
// иначе и пока заголовок неизвестен — fallback (poll_interval_seconds).
func (b *Bot) StartPoller(ctx context.Context, fallback time.Duration) {
	if fallback <= 0 {
		fallback = time.Duration(config.DefaultPollIntervalSec) * time.Second
	}
	b.pollFallback = fallback
	slog.Info("Воркер опроса матчей запущен", slog.Duration("fallback_interval", fallback))

	for {
		delay := b.runPollerCycle(ctx, fallback)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			slog.Info("Воркер опрашивания матчей завершил работу")
			return
		case <-timer.C:
		}
	}
}

type matchEventKind int

const (
	eventNew matchEventKind = iota
	eventStarted
	eventOpponent
	eventTimeChanged
)

type matchEvent struct {
	match       domain.Match
	kind        matchEventKind
	timeChanged bool
	oldTime     time.Time
	oldTeamA    string
	oldTeamB    string
}

// classifyMatchUpdate решает, какое уведомление слать по изменению матча.
// postponed не даёт «новый матч»: иначе связка с CleanOldMatches повторяет
// рассылку каждую минуту. Смена времени или соперника уходит как обычно.
// ok=false — событие для рассылки не нужно.
func classifyMatchUpdate(c domain.MatchChange, status string) (matchEventKind, bool) {
	switch {
	case c.IsNew && status == "postponed":
		return 0, false
	case c.IsNew:
		return eventNew, true
	case status == "running" && c.StatusChanged && c.OldStatus != "running":
		return eventStarted, true
	case c.TeamsChanged:
		return eventOpponent, true
	case c.TimeChanged && !c.MinorTimeShift:
		return eventTimeChanged, true
	default:
		return 0, false
	}
}

func (b *Bot) runPollerCycle(ctx context.Context, fallback time.Duration) time.Duration {
	slog.Debug("Запуск цикла обновления матчей")

	idsToFetch, err := b.storage.GetSubscribedTeamIDs()
	if err != nil {
		slog.Error("Ошибка получения ID команд из БД", slog.Any("error", err))
		return b.applyPollPace(nextPollDelay(-1, 0, 0, fallback, b.paceRestricted()))
	}
	if len(idsToFetch) == 0 {
		_, remaining, limit := b.panda.APIUsage()
		pace := nextPollDelay(remaining, limit, 0, fallback, b.paceRestricted())
		b.maybeNotifyAPIHeadroom(ctx, remaining, limit, pace.Restricted)
		return b.applyPollPace(pace)
	}

	usedBefore, _, _ := b.panda.APIUsage()
	matches, err := b.panda.FetchMatchesByTeamIDs(ctx, idsToFetch)
	usedAfter, remaining, limit := b.panda.APIUsage()
	cycleReqs := usedAfter - usedBefore
	if cycleReqs < 0 {
		// Счётчик used обнулился на границе UTC-часа внутри цикла.
		cycleReqs = usedAfter
	}
	pace := nextPollDelay(remaining, limit, cycleReqs, fallback, b.paceRestricted())
	b.maybeNotifyAPIHeadroom(ctx, remaining, limit, pace.Restricted)
	delay := b.applyPollPace(pace)

	if err != nil {
		slog.Error("Ошибка запроса матчей из API", slog.Any("error", err))
		return delay
	}

	// Собираем ID матчей из ответа API
	apiMatchIDs := make(map[int]bool, len(matches))
	for _, m := range matches {
		apiMatchIDs[m.ID] = true
	}

	// Собираем события цикла, рассылка — одним сообщением на пользователя
	// после цикла (иначе при пачке новых матчей каждому прилетает спам).
	var events []matchEvent
	for _, match := range matches {
		change, err := b.storage.ProcessMatch(match)
		if err != nil {
			slog.Error("Ошибка сохранения матча", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}

		if !change.Any() || match.TeamA == "TBD" || match.TeamB == "TBD" {
			continue
		}

		// Матчи без времени начала (begin_at=null в API) не рассылаем:
		// иначе спамим датой 01.01.0001, а CleanOldMatches все равно их сотрет.
		if match.Time.IsZero() {
			continue
		}

		kind, ok := classifyMatchUpdate(change, match.Status)
		if !ok {
			continue
		}
		events = append(events, matchEvent{
			match:       match,
			kind:        kind,
			timeChanged: change.TimeChanged,
			oldTime:     change.OldTime,
			oldTeamA:    change.OldTeamA,
			oldTeamB:    change.OldTeamB,
		})
	}

	if len(events) > 0 {
		b.broadcastMatchEvents(ctx, events)
	}

	b.storage.CleanStaleRunningMatches(apiMatchIDs)
	b.storage.CleanOldMatches()

	if len(events) > 0 || len(matches) > 0 {
		var started, news int
		for _, ev := range events {
			switch ev.kind {
			case eventStarted:
				started++
			case eventNew:
				news++
			}
		}
		slog.Info("Цикл обновления матчей",
			slog.Int("api_matches", len(matches)),
			slog.Int("events", len(events)),
			slog.Int("started", started),
			slog.Int("new", news),
			slog.Duration("next_delay", delay),
			slog.Bool("pace_restricted", pace.Restricted))
	} else {
		slog.Debug("Цикл обновления матчей",
			slog.Duration("next_delay", delay),
			slog.Int("cycle_reqs", cycleReqs),
			slog.Bool("pace_restricted", pace.Restricted))
	}
	return delay
}

// paceRestricted читает флаг гистерезиса запаса лимита.
func (b *Bot) paceRestricted() bool {
	b.apiWarnMu.Lock()
	defer b.apiWarnMu.Unlock()
	return b.apiPaceRestricted
}

// applyPollPace сохраняет паузу для /admin и возвращает её длительность.
func (b *Bot) applyPollPace(pace pollPace) time.Duration {
	b.pollPaceMu.Lock()
	b.pollPaceKnown = true
	b.pollPaceDelay = pace.Delay
	b.pollPaceMode = pace.Mode
	b.pollPaceRestricted = pace.Restricted
	b.pollPaceMu.Unlock()
	return pace.Delay
}

// maybeNotifyAPIHeadroom шлёт админам сообщение при выключении динамики
// (переход в restricted: остаток ≤ headroom%). Пока ждём остаток > target% — молчим.
func (b *Bot) maybeNotifyAPIHeadroom(ctx context.Context, remaining, limit int, nowRestricted bool) {
	if limit <= 0 || remaining < 0 {
		return
	}

	b.apiWarnMu.Lock()
	notify := shouldNotifyHeadroom(b.apiPaceRestricted, nowRestricted, b.apiWarnAt, time.Now())
	b.apiPaceRestricted = nowRestricted
	if notify {
		b.apiWarnAt = time.Now()
	}
	b.apiWarnMu.Unlock()
	if !notify {
		return
	}

	fallback := b.pollFallback
	if fallback <= 0 {
		fallback = time.Duration(config.DefaultPollIntervalSec) * time.Second
	}
	slog.Info("Опрос матчей на фиксированном интервале: остаток лимита ≤ headroom",
		slog.Int("remaining", remaining),
		slog.Int("limit", limit),
		slog.Int("headroom_pct", apiPaceHeadroomPercent),
		slog.Int("resume_pct", apiPaceTargetPercent),
		slog.Duration("delay", fallback))
	text := fmt.Sprintf("⚠️ Остаток лимита PandaScore ≤ %d%%: %d/%d.\nОпрос на фиксированном интервале %s до остатка > %d%%.",
		apiPaceHeadroomPercent, remaining, limit, fallback.Round(time.Second), apiPaceTargetPercent)
	for id := range b.admins {
		ok, dropped := b.enqueueToUser(ctx, id, text)
		if dropped > 0 {
			slog.Error("Не удалось поставить уведомление о лимите API в очередь",
				slog.Int64("admin_id", id),
				slog.Int("dropped", dropped))
		}
		if !ok {
			return
		}
	}
}

// broadcastMatchEvents раскладывает события цикла по пользователям и шлет
// каждому одно объединенное сообщение (время — в его поясе).
func (b *Bot) broadcastMatchEvents(ctx context.Context, events []matchEvent) {
	// Ссылка HLTV нужна только карточкам "Матчи начались": подтягиваем из кеша.
	var startedIdx []int
	var startedMatches []domain.Match
	for i, ev := range events {
		if ev.kind == eventStarted {
			startedIdx = append(startedIdx, i)
			startedMatches = append(startedMatches, ev.match)
		}
	}
	b.attachHLTV(startedMatches)
	for j, i := range startedIdx {
		events[i].match.HLTVURL = startedMatches[j].HLTVURL
	}

	// Имена YouTube-каналов — для всех карточек событий (из кеша БД).
	allMatches := make([]domain.Match, len(events))
	for i, ev := range events {
		allMatches[i] = ev.match
	}
	b.storage.FillStreamChannels(allMatches)
	for i := range events {
		events[i].match.Streams = allMatches[i].Streams
	}

	userEvents := make(map[int64][]matchEvent)
	for _, ev := range events {
		users, err := b.storage.GetUsersByTeamIDs(ev.match.TeamAID, ev.match.TeamBID)
		if err != nil {
			slog.Error("Ошибка получения подписчиков матча",
				slog.String("team_a", ev.match.TeamA),
				slog.String("team_b", ev.match.TeamB),
				slog.Any("error", err))
			continue
		}
		for _, u := range users {
			userEvents[u] = append(userEvents[u], ev)
		}
	}
	if len(userEvents) == 0 {
		return
	}

	ids := make([]int64, 0, len(userEvents))
	for id := range userEvents {
		ids = append(ids, id)
	}
	offsets, err := b.storage.GetUserOffsets(ids)
	if err != nil {
		slog.Error("Ошибка получения часовых поясов", slog.Any("error", err))
		offsets = make(map[int64]int, len(ids))
	}

	slog.Info("Добавление объединенных уведомлений в очередь",
		slog.Int("events", len(events)),
		slog.Int("recipients", len(userEvents)))

	dropped := 0
	for userID, evts := range userEvents {
		ok, d := b.enqueueToUser(ctx, userID, buildCombinedUpdateMessage(evts, offsets[userID], b.maxStreams))
		dropped += d
		if !ok {
			return
		}
	}

	if dropped > 0 {
		slog.Warn("Очередь рассылки переполнена, часть уведомлений отброшена",
			slog.Int("dropped", dropped),
			slog.Int("recipients", len(userEvents)),
			slog.Int("queue_len", len(b.broadcastCh)),
		)
	}
}

// buildCombinedUpdateMessage собирает все события цикла для одного юзера
// в одно сообщение, группируя по типу. maxStreams: 0 = все стримы.
func buildCombinedUpdateMessage(evts []matchEvent, off int, maxStreams int) string {
	var news, started, opponents, times []matchEvent
	for _, ev := range evts {
		switch ev.kind {
		case eventNew:
			news = append(news, ev)
		case eventStarted:
			started = append(started, ev)
		case eventOpponent:
			opponents = append(opponents, ev)
		case eventTimeChanged:
			times = append(times, ev)
		}
	}
	// Внутри каждой секции — по времени начала, ближайшие сверху.
	byTime := func(a, b matchEvent) bool { return a.match.Time.Before(b.match.Time) }
	sort.Slice(started, func(i, j int) bool { return byTime(started[i], started[j]) })
	sort.Slice(news, func(i, j int) bool { return byTime(news[i], news[j]) })
	sort.Slice(opponents, func(i, j int) bool { return byTime(opponents[i], opponents[j]) })
	sort.Slice(times, func(i, j int) bool { return byTime(times[i], times[j]) })

	var sb strings.Builder
	if len(started) > 0 {
		sb.WriteString("🔴 <b>Матчи начались!</b>\n\n")
		for i, ev := range started {
			if i > 0 {
				sb.WriteString("➖➖➖➖➖➖➖\n")
			}
			teamA := "<b>" + html.EscapeString(ev.match.TeamA) + "</b>"
			teamB := "<b>" + html.EscapeString(ev.match.TeamB) + "</b>"
			sb.WriteString(formatMatchCard(ev.match, teamA, teamB, off, maxStreams) + "\n\n")
		}
	}
	if len(news) > 0 {
		sb.WriteString("🆕 <b>Новые матчи!</b>\n\n")
		for _, ev := range news {
			timeStr := formatTGTime(ev.match.Time, "dt", "15:04 02.01", off)
			sb.WriteString(fmt.Sprintf("🛡 <b>%s</b> vs <b>%s</b>\n⏰ Время: %s\n\n",
				html.EscapeString(ev.match.TeamA), html.EscapeString(ev.match.TeamB), timeStr))
		}
	}
	if len(opponents) > 0 {
		sb.WriteString("🔄 <b>Определились соперники!</b>\n\n")
		for _, ev := range opponents {
			timeStr := formatTGTime(ev.match.Time, "dt", "15:04 02.01", off)
			timeText := fmt.Sprintf("⏰ Время: %s", timeStr)
			if ev.timeChanged {
				oldTimeStr := formatTGTime(ev.oldTime, "dt", "15:04 02.01", off)
				timeText = fmt.Sprintf("<s>Время: %s</s>\n⏰ Новое: %s", oldTimeStr, timeStr)
			}
			sb.WriteString(fmt.Sprintf("<s>%s vs %s</s>\n🛡 <b>%s</b> vs <b>%s</b>\n%s\n\n",
				html.EscapeString(ev.oldTeamA), html.EscapeString(ev.oldTeamB),
				html.EscapeString(ev.match.TeamA), html.EscapeString(ev.match.TeamB), timeText))
		}
	}
	if len(times) > 0 {
		sb.WriteString("⚠️ <b>Время матчей изменено!</b>\n\n")
		for _, ev := range times {
			timeStr := formatTGTime(ev.match.Time, "dt", "15:04 02.01", off)
			oldTimeStr := formatTGTime(ev.oldTime, "dt", "15:04 02.01", off)
			sb.WriteString(fmt.Sprintf("🛡 <b>%s</b> vs <b>%s</b>\n<s>Старое время: %s</s>\n⏰ Новое время: %s\n\n",
				html.EscapeString(ev.match.TeamA), html.EscapeString(ev.match.TeamB), oldTimeStr, timeStr))
		}
	}
	return strings.TrimSuffix(sb.String(), "\n")
}
