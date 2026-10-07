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

func (b *Bot) StartPoller(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Duration(config.DefaultPollIntervalSec) * time.Second
	}
	slog.Info("Воркер опроса матчей запущен", slog.Duration("interval", interval))
	b.runPollerCycle(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Воркер опрашивания матчей завершил работу")
			return
		case <-ticker.C:
			b.runPollerCycle(ctx)
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

func (b *Bot) runPollerCycle(ctx context.Context) {
	slog.Debug("Запуск цикла обновления матчей")

	idsToFetch, err := b.storage.GetSubscribedTeamIDs()
	if err != nil {
		slog.Error("Ошибка получения ID команд из БД", slog.Any("error", err))
		return
	}
	if len(idsToFetch) == 0 {
		return
	}

	// Обновляем предстоящие матчи
	matches, err := b.panda.FetchMatchesByTeamIDs(ctx, idsToFetch)
	if err != nil {
		slog.Error("Ошибка запроса матчей из API", slog.Any("error", err))
		return
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
		isNew, timeChanged, teamsChanged, statusChanged, oldTime, oldTeamA, oldTeamB, oldStatus, err := b.storage.ProcessMatch(match)
		if err != nil {
			slog.Error("Ошибка сохранения матча", slog.Int("match_id", match.ID), slog.Any("error", err))
			continue
		}

		if (!isNew && !timeChanged && !teamsChanged && !statusChanged) || match.TeamA == "TBD" || match.TeamB == "TBD" {
			continue
		}

		// Матчи без времени начала (begin_at=null в API) не рассылаем:
		// иначе спамим датой 01.01.0001, а CleanOldMatches все равно их сотрет.
		if match.Time.IsZero() {
			continue
		}

		ev := matchEvent{match: match, timeChanged: timeChanged, oldTime: oldTime, oldTeamA: oldTeamA, oldTeamB: oldTeamB}
		switch {
		case isNew:
			ev.kind = eventNew
		case match.Status == "running" && statusChanged && oldStatus != "running":
			ev.kind = eventStarted
		case teamsChanged:
			ev.kind = eventOpponent
		case timeChanged:
			ev.kind = eventTimeChanged
		default:
			continue
		}
		events = append(events, ev)
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
			slog.Int("new", news))
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
