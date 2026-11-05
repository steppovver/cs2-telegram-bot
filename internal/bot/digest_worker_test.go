package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

// digestStore отвечает только на вызовы дайджеста. Остальные методы Storage
// не вызываются: встраивание интерфейса оставит их nil и уронит тест, если
// воркер начнет ходить в БД шире.
type digestStore struct {
	Storage
	until           int64
	withTournaments bool
	hasTournaments  bool
	upcoming        []domain.Match
	marked          bool
}

func (d *digestStore) HasTournamentSubscriptions(int64) (bool, error) {
	return d.hasTournaments, nil
}

func (d *digestStore) GetScheduleMatches(_ int64, untilUnix int64, withTournaments bool) ([]domain.Match, []domain.Match, error) {
	d.until = untilUnix
	d.withTournaments = withTournaments
	var upcoming []domain.Match
	for _, m := range d.upcoming {
		if untilUnix == 0 || m.Time.Unix() <= untilUnix {
			upcoming = append(upcoming, m)
		}
	}
	return nil, upcoming, nil
}

func (d *digestStore) GetUserSubscriptions(int64) ([]domain.TeamInfo, error) {
	return nil, nil
}

func (d *digestStore) MarkDigestSent(int64, string) error {
	d.marked = true
	return nil
}

func drainBroadcast(ch <-chan BroadcastTask) string {
	var b strings.Builder
	for {
		select {
		case task := <-ch:
			b.WriteString(task.Text)
		default:
			return b.String()
		}
	}
}

func TestDigestUntilCoversRestOfNextDay(t *testing.T) {
	off := 3
	loc := time.FixedZone("UTC+3", off*3600)
	// Дайджест 05.11.2026 10:00 UTC+3.
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, loc)
	until := digestUntil(now, off)
	want := time.Date(2026, 11, 6, 23, 59, 59, 0, loc)
	if !until.Equal(want) {
		t.Fatalf("until=%s, want %s", until.In(loc).Format(time.RFC3339), want.Format(time.RFC3339))
	}

	morning := time.Date(2026, 11, 6, 9, 30, 0, 0, loc)
	afternoon := time.Date(2026, 11, 6, 12, 45, 0, 0, loc)
	nextDay := time.Date(2026, 11, 7, 9, 30, 0, 0, loc)
	if morning.After(until) || afternoon.After(until) {
		t.Fatalf("оба матча 6 ноября должны входить: until=%s", until.In(loc).Format(time.RFC3339))
	}
	if !afternoon.After(now.Add(24 * time.Hour)) {
		t.Fatal("12:45 6 ноября позже жёстких 24 часов от 10:00")
	}
	if !nextDay.After(until) {
		t.Fatal("матч 7 ноября должен остаться за окном дайджеста")
	}

	late := time.Date(2026, 11, 5, 23, 0, 0, 0, loc)
	if !digestUntil(late, off).Equal(want) {
		t.Fatalf("вечерний дайджест тоже до конца 6 ноября, got %s", digestUntil(late, off).In(loc).Format(time.RFC3339))
	}

	// Граница месяца и отрицательный пояс.
	jan := time.Date(2026, 1, 31, 10, 0, 0, 0, loc)
	janWant := time.Date(2026, 2, 1, 23, 59, 59, 0, loc)
	if !digestUntil(jan, off).Equal(janWant) {
		t.Fatalf("январь: %s", digestUntil(jan, off).In(loc).Format(time.RFC3339))
	}
	west := time.FixedZone("UTC-5", -5*3600)
	westNow := time.Date(2026, 11, 5, 10, 0, 0, 0, west)
	westWant := time.Date(2026, 11, 6, 23, 59, 59, 0, west)
	if !digestUntil(westNow, -5).Equal(westWant) {
		t.Fatalf("UTC-5: %s", digestUntil(westNow, -5).In(west).Format(time.RFC3339))
	}
}

func TestProcessDigestUserKeepsSameDayMatches(t *testing.T) {
	off := 3
	loc := time.FixedZone("", off*3600)
	local := time.Now().In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	store := &digestStore{
		hasTournaments: true,
		upcoming: []domain.Match{
			{
				ID: 1, TeamA: "PARIVISION", TeamB: "Natus Vincere",
				Time:       start.Add(24*time.Hour + 9*time.Hour + 30*time.Minute),
				Tournament: "IEM Beijing 2026", TournamentID: 9, NumberOfGames: 3,
			},
			{
				ID: 2, TeamA: "FURIA", TeamB: "BetBoom Team",
				Time:       start.Add(24*time.Hour + 12*time.Hour + 45*time.Minute),
				Tournament: "IEM Beijing 2026", TournamentID: 9, NumberOfGames: 3,
			},
			{
				ID: 3, TeamA: "TheMongolz", TeamB: "TBD",
				Time:       start.Add(72*time.Hour + 9*time.Hour + 30*time.Minute),
				Tournament: "IEM Beijing 2026", TournamentID: 9, NumberOfGames: 3,
			},
		},
	}
	b := &Bot{storage: store, broadcastCh: make(chan BroadcastTask, 8)}
	before := time.Now()
	if !b.processDigestUser(context.Background(), 7, off, "2026-11-05-07") {
		t.Fatal("ожидали дайджест с матчами")
	}
	lo := digestUntil(before, off).Unix()
	hi := digestUntil(time.Now(), off).Unix()
	if store.until < lo || store.until > hi {
		t.Fatalf("until=%d, want [%d, %d]", store.until, lo, hi)
	}
	if !store.withTournaments {
		t.Fatal("плей-офф подписанных турниров должен входить в дайджест")
	}
	text := drainBroadcast(b.broadcastCh)
	for _, want := range []string{"PARIVISION", "Natus Vincere", "FURIA", "BetBoom Team", "Дайджест на 24 часа"} {
		if !strings.Contains(text, want) {
			t.Errorf("нет %q в %q", want, text)
		}
	}
	if strings.Contains(text, "TheMongolz") {
		t.Errorf("матч через день попал в дайджест: %q", text)
	}
}

func TestProcessDigestUserEmptyStillMarksSent(t *testing.T) {
	store := &digestStore{}
	b := &Bot{storage: store, broadcastCh: make(chan BroadcastTask, 1)}
	if b.processDigestUser(context.Background(), 7, 3, "slot") {
		t.Fatal("пустой дайджест не отправляем")
	}
	if store.until == 0 {
		t.Fatal("пустое окно не должно снимать границу")
	}
	if !store.marked {
		t.Fatal("пустой слот должен быть отмечен")
	}
	if text := drainBroadcast(b.broadcastCh); text != "" {
		t.Fatalf("в очередь ушло %q", text)
	}
}
