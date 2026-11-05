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
	return nil, d.upcoming, nil
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

func TestProcessDigestUserUsesFullSchedule(t *testing.T) {
	store := &digestStore{
		hasTournaments: true,
		upcoming: []domain.Match{
			{
				ID: 1, TeamA: "PARIVISION", TeamB: "Natus Vincere",
				Time: time.Now().Add(23*time.Hour + 30*time.Minute),
				Tournament: "IEM Beijing 2026", TournamentID: 9, NumberOfGames: 3,
			},
			{
				ID: 2, TeamA: "FURIA", TeamB: "BetBoom Team",
				Time: time.Now().Add(26*time.Hour + 45*time.Minute),
				Tournament: "IEM Beijing 2026", TournamentID: 9, NumberOfGames: 3,
			},
			{
				ID: 3, TeamA: "Liquid", TeamB: "BC.Game Esports",
				Time: time.Now().Add(19 * 24 * time.Hour),
				Tournament: "PGL Major Singapore 2026", TournamentID: 11, NumberOfGames: 1,
			},
		},
	}
	b := &Bot{storage: store, broadcastCh: make(chan BroadcastTask, 8)}
	if !b.processDigestUser(context.Background(), 7, 3, "2026-11-05-07") {
		t.Fatal("ожидали дайджест с матчами")
	}
	if store.until != 0 {
		t.Fatalf("until=%d, дайджест должен брать расписание без границы", store.until)
	}
	if !store.withTournaments {
		t.Fatal("плей-офф подписанных турниров должен входить в дайджест")
	}
	if !store.marked {
		t.Fatal("дайджест не отмечен отправленным")
	}
	text := drainBroadcast(b.broadcastCh)
	for _, want := range []string{"PARIVISION", "Natus Vincere", "FURIA", "BetBoom Team", "Liquid", "BC.Game Esports", "Дайджест"} {
		if !strings.Contains(text, want) {
			t.Errorf("нет %q в %q", want, text)
		}
	}
	if strings.Contains(text, "24 часа") {
		t.Errorf("заголовок всё ещё про 24 часа: %q", text)
	}
}

func TestProcessDigestUserEmptyStillMarksSent(t *testing.T) {
	store := &digestStore{}
	b := &Bot{storage: store, broadcastCh: make(chan BroadcastTask, 1)}
	if b.processDigestUser(context.Background(), 7, 3, "slot") {
		t.Fatal("пустой дайджест не отправляем")
	}
	if store.until != 0 {
		t.Fatalf("until=%d, дайджест должен брать расписание без границы", store.until)
	}
	if !store.marked {
		t.Fatal("пустой слот должен быть отмечен")
	}
	if text := drainBroadcast(b.broadcastCh); text != "" {
		t.Fatalf("в очередь ушло %q", text)
	}
}
