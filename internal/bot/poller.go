package bot

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"cs2bot/internal/config"
	"cs2bot/internal/domain"
)

// pollHistoryWindow — глубина первого/сброшенного опроса. Совпадает с
// горизонтом CleanOldMatches (24 часа): старше в БД всё равно ничего не лежит.
const pollHistoryWindow = 24 * time.Hour

// StartPoller опрашивает предстоящие/live/завершённые матчи одним запросом.
// Пауза между циклами динамическая (apiPaceTargetPercent лимита в час) с
// гистерезисом запаса; иначе и пока заголовок неизвестен — fallback
// (poll_interval_seconds). Курсор — begin_at самого раннего незавершённого:
// следующий цикл не тянет finished старше этой границы (счёт там застывает).
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

type matchEvent struct {
	match       domain.Match
	kind        domain.EventKind
	timeChanged bool
	oldTime     time.Time
	oldTeamA    string
	oldTeamB    string
	// favA/favB — какая сторона любимая у получателя этой копии события.
	favA bool
	favB bool
}

// mergePlayoffMatches добавляет к матчам команд плей-офф матчи серий.
// Групповые стадии чужих команд не сохраняем: их слишком много.
func mergePlayoffMatches(teamMatches, serieMatches []domain.Match) []domain.Match {
	seen := make(map[int]bool, len(teamMatches))
	for _, m := range teamMatches {
		seen[m.ID] = true
	}
	for _, m := range serieMatches {
		if !m.IsPlayoff || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		teamMatches = append(teamMatches, m)
	}
	return teamMatches
}

// resolvePollSince — левая граница запроса: max(курсор, now-24h).
// Нулевой курсор (старт / сброс) = полный пол.
func resolvePollSince(cursor, now time.Time) time.Time {
	floor := now.Add(-pollHistoryWindow)
	if cursor.IsZero() || cursor.Before(floor) {
		return floor
	}
	return cursor
}

// earliestUnfinishedBegin — самый ранний begin_at среди not_started/running/
// postponed. Матчи без времени пропускаем: иначе zero time стянет курсор
// в эпоху и сломает стоп. ok=false — незавершённых нет.
func earliestUnfinishedBegin(matches []domain.Match) (time.Time, bool) {
	var earliest time.Time
	found := false
	for _, m := range matches {
		switch m.Status {
		case "not_started", "running", "postponed":
		default:
			continue
		}
		if m.Time.IsZero() {
			continue
		}
		if !found || m.Time.Before(earliest) {
			earliest = m.Time
			found = true
		}
	}
	return earliest, found
}

// pollScopeKey — отпечаток команд и подписанных серий. Смена любого
// набора сбрасывает курсор, иначе новый турнир не доберет окно 24 часа.
func pollScopeKey(teamIDs, serieIDs []string) string {
	return teamIDsKey(teamIDs) + "|" + teamIDsKey(serieIDs)
}

// teamIDsKey — стабильный отпечаток набора ID для сброса курсора.
func teamIDsKey(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// pollSinceForTeams читает курсор; при смене команд или серий сбрасывает его.
func (b *Bot) pollSinceForTeams(teamIDs, serieIDs []string, now time.Time) time.Time {
	key := pollScopeKey(teamIDs, serieIDs)
	b.pollCursorMu.Lock()
	defer b.pollCursorMu.Unlock()
	if key != b.pollCursorTeams {
		b.pollCursor = time.Time{}
		b.pollCursorTeams = key
	}
	return resolvePollSince(b.pollCursor, now)
}

// rememberPollCursor сохраняет границу следующего цикла. Нет незавершённых —
// курсор = now (прошлые finished больше не читаем).
func (b *Bot) rememberPollCursor(matches []domain.Match, now time.Time) {
	next, ok := earliestUnfinishedBegin(matches)
	if !ok {
		next = now
	}
	b.pollCursorMu.Lock()
	b.pollCursor = next
	b.pollCursorMu.Unlock()
}

// resetPollCursor сбрасывает курсор: следующий цикл снова на 24 часа.
func (b *Bot) resetPollCursor() {
	b.pollCursorMu.Lock()
	b.pollCursor = time.Time{}
	b.pollCursorMu.Unlock()
}

type pollScope struct {
	teamIDs  []string
	serieIDs []string
	seriesOK bool
	now      time.Time
	since    time.Time
}

func (b *Bot) runPollerCycle(ctx context.Context, fallback time.Duration) time.Duration {
	slog.Debug("Запуск цикла обновления матчей")

	scope, delay, done := b.openPollScope(ctx, fallback)
	if done {
		return delay
	}

	matches, seriesOK, delay, pace, cycleReqs, err := b.fetchPollMatches(ctx, fallback, scope)
	if err != nil {
		slog.Error("Ошибка запроса матчей из API", slog.Any("error", err))
		// Неполный ответ — курсор не сужаем: иначе живой матч ниже границы
		// выпадет до следующего полного прохода.
		b.resetPollCursor()
		return delay
	}

	events := b.collectPollEvents(matches)
	if len(events) > 0 {
		b.broadcastMatchEvents(ctx, events)
	}
	b.housekeepPoll(seriesOK, matches, scope.now)
	b.logPollCycle(events, matches, scope.since, delay, pace, cycleReqs)
	return delay
}

// openPollScope читает, кого опрашивать. done=true — цикл закончен,
// delay уже посчитан (ошибка команд или не на кого подписываться).
func (b *Bot) openPollScope(ctx context.Context, fallback time.Duration) (pollScope, time.Duration, bool) {
	idsToFetch, err := b.storage.GetSubscribedTeamIDs()
	if err != nil {
		slog.Error("Ошибка получения ID команд из БД", slog.Any("error", err))
		delay := b.applyPollPace(nextPollDelay(-1, 0, 0, fallback, b.paceRestricted()))
		return pollScope{}, delay, true
	}
	// Без полного ответа по сериям нельзя чистить зависшие running:
	// идущие турнирные матчи ушли бы в post_match.
	serieIDs, err := b.storage.GetActiveSubscribedTournamentIDs()
	seriesOK := err == nil
	if err != nil {
		slog.Error("Ошибка получения подписанных турниров из БД", slog.Any("error", err))
	}
	if len(idsToFetch) == 0 && len(serieIDs) == 0 {
		b.resetPollCursor()
		_, remaining, limit := b.panda.APIUsage()
		pace := nextPollDelay(remaining, limit, 0, fallback, b.paceRestricted())
		b.maybeNotifyAPIHeadroom(ctx, remaining, limit, pace.Restricted)
		return pollScope{}, b.applyPollPace(pace), true
	}
	now := time.Now()
	return pollScope{
		teamIDs:  idsToFetch,
		serieIDs: serieIDs,
		seriesOK: seriesOK,
		now:      now,
		since:    b.pollSinceForTeams(idsToFetch, serieIDs, now),
	}, 0, false
}

// fetchPollMatches забирает матчи команд и плей-офф подписанных серий
// и считает паузу до следующего цикла. seriesOK становится false, если
// запрос серий не удался.
func (b *Bot) fetchPollMatches(ctx context.Context, fallback time.Duration, scope pollScope) ([]domain.Match, bool, time.Duration, pollPace, int, error) {
	seriesOK := scope.seriesOK
	usedBefore, _, _ := b.panda.APIUsage()
	matches, err := b.panda.FetchMatchesByTeamIDs(ctx, scope.teamIDs, scope.since)
	if err == nil && len(scope.serieIDs) > 0 {
		serieMatches, serr := b.panda.FetchMatchesBySerieIDs(ctx, scope.serieIDs, scope.since)
		if serr != nil {
			slog.Error("Ошибка запроса матчей турниров из API", slog.Any("error", serr))
			seriesOK = false
		} else {
			matches = mergePlayoffMatches(matches, serieMatches)
		}
	}
	usedAfter, remaining, limit := b.panda.APIUsage()
	cycleReqs := usedAfter - usedBefore
	if cycleReqs < 0 {
		// Счётчик used обнулился на границе UTC-часа внутри цикла.
		cycleReqs = usedAfter
	}
	pace := nextPollDelay(remaining, limit, cycleReqs, fallback, b.paceRestricted())
	b.maybeNotifyAPIHeadroom(ctx, remaining, limit, pace.Restricted)
	delay := b.applyPollPace(pace)
	b.logAPIUsage()
	return matches, seriesOK, delay, pace, cycleReqs, err
}

// collectPollEvents пишет матчи и собирает события для одной рассылки.
func (b *Bot) collectPollEvents(matches []domain.Match) []matchEvent {
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

		kind, ok := domain.ClassifyMatchUpdate(change, match.Status)
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
	return events
}

// housekeepPoll чистит зависшие и старые строки и двигает курсор опроса.
func (b *Bot) housekeepPoll(seriesOK bool, matches []domain.Match, now time.Time) {
	if seriesOK {
		apiMatchIDs := make(map[int]bool, len(matches))
		for _, m := range matches {
			apiMatchIDs[m.ID] = true
		}
		b.storage.CleanStaleRunningMatches(apiMatchIDs)
	}
	b.storage.CleanOldMatches()
	b.storage.CleanFinishedTournaments()
	if seriesOK {
		b.rememberPollCursor(matches, now)
		return
	}
	// Ответ по сериям неполный: курсор не сужаем, иначе плей-офф
	// ниже границы выпадет до следующего полного прохода.
	b.resetPollCursor()
}

func (b *Bot) logPollCycle(events []matchEvent, matches []domain.Match, since time.Time, delay time.Duration, pace pollPace, cycleReqs int) {
	if len(events) > 0 || len(matches) > 0 {
		var started, news int
		for _, ev := range events {
			switch ev.kind {
			case domain.EventStarted:
				started++
			case domain.EventNew:
				news++
			}
		}
		slog.Info("Цикл обновления матчей",
			slog.Int("api_matches", len(matches)),
			slog.Int("events", len(events)),
			slog.Int("started", started),
			slog.Int("new", news),
			slog.Time("since", since),
			slog.Duration("next_delay", delay),
			slog.Bool("pace_restricted", pace.Restricted))
		return
	}
	slog.Debug("Цикл обновления матчей",
		slog.Duration("next_delay", delay),
		slog.Int("cycle_reqs", cycleReqs),
		slog.Bool("pace_restricted", pace.Restricted))
}

// logAPIUsage пишет сводку использования REST API.
// remaining — правда от сервера, used — запросы этого процесса с запуска
// (рестарты и teams_puller мимо счетчика).
func (b *Bot) logAPIUsage() {
	used, remaining, limit := b.panda.APIUsage()
	slog.Info("Использование PandaScore API",
		slog.Int("remaining", remaining),
		slog.Int("limit", limit),
		slog.Int("used_since_start", used))
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
		if ev.kind == domain.EventStarted {
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
		fans, err := b.matchEventRecipients(ev)
		if err != nil {
			slog.Error("Ошибка получения подписчиков матча",
				slog.String("team_a", ev.match.TeamA),
				slog.String("team_b", ev.match.TeamB),
				slog.Any("error", err))
			continue
		}
		for _, fan := range fans {
			copyEv := ev
			copyEv.favA = fan.FavoriteA
			copyEv.favB = fan.FavoriteB
			userEvents[fan.UserID] = append(userEvents[fan.UserID], copyEv)
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
		ok, d := b.enqueueToUser(ctx, userID, buildCombinedUpdateMessage(evts, offsets[userID]))
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

// matchEventRecipients — кому слать событие. Любимые получают все события,
// интересные — только старт. Подписчики турнира добавляются на старт плей-офф.
func (b *Bot) matchEventRecipients(ev matchEvent) ([]domain.MatchFan, error) {
	fans, err := b.storage.GetMatchFans(ev.match.TeamAID, ev.match.TeamBID)
	if err != nil {
		return nil, fmt.Errorf("подписчики команд: %w", err)
	}
	fans = domain.FansForMatchEvent(ev.kind, fans)
	if ev.kind != domain.EventStarted || !ev.match.IsPlayoff || ev.match.TournamentID == 0 {
		return fans, nil
	}
	tUsers, err := b.storage.GetUsersByTournamentID(ev.match.TournamentID)
	if err != nil {
		return nil, fmt.Errorf("подписчики турнира: %w", err)
	}
	seen := make(map[int64]bool, len(fans))
	for _, f := range fans {
		seen[f.UserID] = true
	}
	for _, u := range tUsers {
		if !seen[u] {
			seen[u] = true
			fans = append(fans, domain.MatchFan{UserID: u})
		}
	}
	return fans, nil
}

// buildCombinedUpdateMessage собирает все события цикла для одного юзера
// в одно сообщение, группируя по типу.
func buildCombinedUpdateMessage(evts []matchEvent, off int) string {
	var news, started, opponents, times []matchEvent
	for _, ev := range evts {
		switch ev.kind {
		case domain.EventNew:
			news = append(news, ev)
		case domain.EventStarted:
			started = append(started, ev)
		case domain.EventOpponent:
			opponents = append(opponents, ev)
		case domain.EventTimeChanged:
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
	appendEventSection(&sb, "🔴 <b>Матчи начались!</b>\n\n", started, true, func(ev matchEvent) string {
		return formatMatchCard(ev.match, ev.match.TeamA, ev.match.TeamB, pushMark(ev.favA), pushMark(ev.favB), true, nil) + "\n\n"
	})
	appendEventSection(&sb, "🆕 <b>Новые матчи!</b>\n\n", news, false, func(ev matchEvent) string {
		timeStr := formatTGTime(ev.match.Time, "dt", "15:04 02.01", off)
		return fmt.Sprintf("🛡 %s vs %s\n⏰ Время: %s\n\n",
			renderTeam(ev.match.TeamA, pushMark(ev.favA)), renderTeam(ev.match.TeamB, pushMark(ev.favB)), timeStr)
	})
	appendEventSection(&sb, "🔄 <b>Определились соперники!</b>\n\n", opponents, false, func(ev matchEvent) string {
		timeStr := formatTGTime(ev.match.Time, "dt", "15:04 02.01", off)
		timeText := fmt.Sprintf("⏰ Время: %s", timeStr)
		if ev.timeChanged {
			oldTimeStr := formatTGTime(ev.oldTime, "dt", "15:04 02.01", off)
			timeText = fmt.Sprintf("<s>Время: %s</s>\n⏰ Новое: %s", oldTimeStr, timeStr)
		}
		return fmt.Sprintf("<s>%s vs %s</s>\n🛡 %s vs %s\n%s\n\n",
			renderTeam(ev.oldTeamA, teamPlain), renderTeam(ev.oldTeamB, teamPlain),
			renderTeam(ev.match.TeamA, pushMark(ev.favA)), renderTeam(ev.match.TeamB, pushMark(ev.favB)), timeText)
	})
	appendEventSection(&sb, "⚠️ <b>Время матчей изменено!</b>\n\n", times, false, func(ev matchEvent) string {
		timeStr := formatTGTime(ev.match.Time, "dt", "15:04 02.01", off)
		oldTimeStr := formatTGTime(ev.oldTime, "dt", "15:04 02.01", off)
		return fmt.Sprintf("🛡 %s vs %s\n<s>Старое время: %s</s>\n⏰ Новое время: %s\n\n",
			renderTeam(ev.match.TeamA, pushMark(ev.favA)), renderTeam(ev.match.TeamB, pushMark(ev.favB)), oldTimeStr, timeStr)
	})
	return strings.TrimSuffix(sb.String(), "\n")
}

// appendEventSection пишет заголовок секции и строки событий.
// separate ставит разделитель между карточками старта. Текст строки задаёт line.
func appendEventSection(sb *strings.Builder, title string, evts []matchEvent, separate bool, line func(matchEvent) string) {
	if len(evts) == 0 {
		return
	}
	sb.WriteString(title)
	for i, ev := range evts {
		if separate && i > 0 {
			sb.WriteString("➖➖➖➖➖➖➖\n")
		}
		sb.WriteString(line(ev))
	}
}
