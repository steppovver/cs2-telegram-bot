package storage

import (
	"slices"
	"testing"
	"time"

	"cs2bot/internal/domain"
)

func putTournamentMatch(t *testing.T, s *Storage, m domain.Match) {
	t.Helper()
	if m.Tournament == "" {
		m.Tournament = "Tournament"
	}
	if _, err := s.ProcessMatch(m); err != nil {
		t.Fatalf("ProcessMatch(%d): %v", m.ID, err)
	}
}

func matchIDs(ms []domain.Match) []int {
	var ids []int
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestTournamentPlayoffMatches(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()
	serieBegin := now.Add(-2 * 24 * time.Hour)
	serieEnd := now.Add(3 * 24 * time.Hour)
	if err := s.Subscribe(1, 1, "Fav"); err != nil {
		t.Fatal(err)
	}

	base := func(id, a, b, serie int, playoff bool, status string, at time.Time) domain.Match {
		return domain.Match{
			ID: id, TeamA: "A", TeamB: "B", TeamAID: a, TeamBID: b, Time: at, Status: status,
			TournamentID: serie, TournamentBeginAt: serieBegin, TournamentEndAt: serieEnd, IsPlayoff: playoff,
		}
	}
	putTournamentMatch(t, s, base(1, 1, 2, 100, false, "not_started", now.Add(time.Hour)))   // любимая, группа
	putTournamentMatch(t, s, base(2, 3, 4, 100, true, "not_started", now.Add(2*time.Hour)))  // плей-офф серии
	putTournamentMatch(t, s, base(3, 5, 6, 100, false, "not_started", now.Add(3*time.Hour))) // чужая группа
	putTournamentMatch(t, s, base(4, 7, 8, 200, true, "not_started", now.Add(4*time.Hour)))  // чужой турнир
	putTournamentMatch(t, s, base(5, 3, 5, 100, true, "finished", now.Add(-3*time.Hour)))    // счет плей-офф

	_, upcoming, err := s.GetScheduleMatches(1, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := matchIDs(upcoming); !slices.Equal(got, []int{1}) {
		t.Errorf("без подписки на турнир: %v, want [1]", got)
	}

	if err := s.SubscribeTournament(1, 100); err != nil {
		t.Fatal(err)
	}
	_, upcoming, err = s.GetScheduleMatches(1, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := matchIDs(upcoming); !slices.Equal(got, []int{1}) {
		t.Errorf("withTournaments=false: %v, want [1]", got)
	}
	_, upcoming, err = s.GetScheduleMatches(1, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := matchIDs(upcoming); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("withTournaments=true: %v, want [1 2]", got)
	}
	for _, m := range upcoming {
		if m.ID == 2 && (!m.IsPlayoff || !m.TournamentEndAt.Equal(time.Unix(serieEnd.Unix(), 0))) {
			t.Errorf("матч 2: IsPlayoff=%v TournamentEndAt=%v", m.IsPlayoff, m.TournamentEndAt)
		}
	}

	score, err := s.GetScoreMatches(1, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := matchIDs(score); !slices.Equal(got, []int{5}) {
		t.Errorf("счет: %v, want [5]", got)
	}
	score, err = s.GetScoreMatches(1, false)
	if err != nil || len(score) != 0 {
		t.Errorf("счет без турниров: %v, %v", matchIDs(score), err)
	}

	users, err := s.GetUsersByTournamentID(100)
	if err != nil || !slices.Equal(users, []int64{1}) {
		t.Errorf("GetUsersByTournamentID = %v, %v", users, err)
	}
}

func TestGetTournamentsForUser(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()
	if err := s.Subscribe(1, 1, "Fav"); err != nil {
		t.Fatal(err)
	}
	put := func(id, a, serie int, begin, end time.Time) {
		putTournamentMatch(t, s, domain.Match{
			ID: id, TeamA: "A", TeamB: "B", TeamAID: a, TeamBID: 99, Time: now.Add(-time.Hour), Status: "finished",
			TournamentID: serie, Tournament: "Serie", TournamentBeginAt: begin, TournamentEndAt: end,
		})
	}
	put(1, 1, 100, now.Add(-5*24*time.Hour), now.Add(2*24*time.Hour))   // идет, любимая играла
	put(2, 1, 101, now.Add(-20*24*time.Hour), now.Add(-2*24*time.Hour)) // закончился
	put(3, 2, 102, now.Add(-time.Hour), now.Add(24*time.Hour))          // без любимых
	put(4, 1, 103, now.Add(-10*24*time.Hour), time.Time{})              // без end_at, недавний

	// Матчи удаляются через сутки, связь с командой должна остаться.
	if _, err := s.db.Exec(`DELETE FROM matches`); err != nil {
		t.Fatal(err)
	}
	if err := s.SubscribeTournament(1, 102); err != nil {
		t.Fatal(err)
	}

	ts, err := s.GetTournamentsForUser(1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for _, tr := range ts {
		ids = append(ids, tr.ID)
		if tr.Subscribed != (tr.ID == 102) {
			t.Errorf("турнир %d: Subscribed=%v", tr.ID, tr.Subscribed)
		}
	}
	if !slices.Equal(ids, []int{103, 100, 102}) {
		t.Errorf("турниры = %v, want [103 100 102] по времени старта", ids)
	}

	active, err := s.GetActiveSubscribedTournamentIDs()
	if err != nil || !slices.Equal(active, []string{"102"}) {
		t.Errorf("GetActiveSubscribedTournamentIDs = %v, %v", active, err)
	}

	// Обновление без end_at не стирает известное окончание.
	put(5, 1, 100, now.Add(-5*24*time.Hour), time.Time{})
	var end int64
	if err := s.db.QueryRow(`SELECT end_at FROM tournaments WHERE id = 100`).Scan(&end); err != nil || end == 0 {
		t.Errorf("end_at турнира 100 = %d, %v", end, err)
	}
}

func TestCleanFinishedTournaments(t *testing.T) {
	s := openTestStorage(t)
	now := time.Now()
	put := func(id, serie int, begin, end time.Time) {
		putTournamentMatch(t, s, domain.Match{
			ID: id, TeamA: "A", TeamB: "B", TeamAID: 1, TeamBID: 2, Time: now, Status: "finished",
			TournamentID: serie, TournamentBeginAt: begin, TournamentEndAt: end,
		})
		if err := s.SubscribeTournament(1, serie); err != nil {
			t.Fatal(err)
		}
	}
	put(1, 100, now.Add(-10*24*time.Hour), now.Add(-8*24*time.Hour)) // закончился > 7 дней назад
	put(2, 101, now.Add(-10*24*time.Hour), now.Add(-2*24*time.Hour)) // недавно
	put(3, 102, now.Add(-61*24*time.Hour), time.Time{})              // без end_at, старый
	put(4, 103, now.Add(-20*24*time.Hour), time.Time{})              // без end_at, свежий

	if _, err := s.cleanFinishedTournaments(now); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tournament_teams", "tournament_subscriptions"} {
		rows, err := s.db.Query(`SELECT DISTINCT tournament_id FROM ` + table + ` ORDER BY tournament_id`)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		rows.Close()
		if !slices.Equal(ids, []int{101, 103}) {
			t.Errorf("%s: %v, want [101 103]", table, ids)
		}
	}
}

func TestTournamentViewAndCascade(t *testing.T) {
	s := openTestStorage(t)
	if v, err := s.GetTournamentView(1); err != nil || v {
		t.Fatalf("дефолт: %v, %v; want false", v, err)
	}
	if err := s.SetUserOffset(1, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTournamentView(1, true); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetTournamentView(1); err != nil || !v {
		t.Errorf("после включения: %v, %v", v, err)
	}
	if off, _ := s.GetUserOffset(1); off != 5 {
		t.Errorf("режим затер пояс: %d", off)
	}

	if err := s.SubscribeTournament(1, 100); err != nil {
		t.Fatal(err)
	}
	if has, err := s.HasTournamentSubscriptions(1); err != nil || !has {
		t.Errorf("HasTournamentSubscriptions = %v, %v", has, err)
	}
	if err := s.RemoveUser(1); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tournament_subscriptions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("после RemoveUser подписок на турниры: %d, %v", n, err)
	}
}
