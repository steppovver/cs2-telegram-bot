package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"cs2bot/internal/domain"
)

// Subscribe создает или обновляет подписку. favorite=false — интересная
// (только старт матча), true — любимая. subscribed_at ставится в текущее
// время, чтобы клик сразу после подписки мог повысить уровень.
func (s *Storage) Subscribe(userID, teamID int64, teamName string, favorite bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO teams (id, name) VALUES (?, ?)`, teamID, teamName); err != nil {
		return err
	}
	fav := 0
	if favorite {
		fav = 1
	}
	if _, err := tx.Exec(`
		INSERT INTO subscriptions (user_id, team_id, favorite, subscribed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, team_id) DO UPDATE SET
			favorite = excluded.favorite,
			subscribed_at = excluded.subscribed_at
	`, userID, teamID, fav, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) Unsubscribe(userID, teamID int64) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE user_id = ? AND team_id = ?`, userID, teamID)
	return err
}

// RemoveUser удаляет пользователя целиком. Подписки, настройки дайджеста
// и часовой пояс чистятся каскадом по FOREIGN KEY ... ON DELETE CASCADE.
// Вызывается рассылкой при мертвых получателях (бан бота, удаление чата).
func (s *Storage) RemoveUser(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, userID)
	return err
}

// GetStats возвращает счетчики для админ-панели тремя COUNT-запросами.
// Таблицы маленькие, отдельный запрос на каждую дешевле JOIN-агрегации.
func (s *Storage) GetStats() (domain.BotStats, error) {
	var st domain.BotStats
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&st.Users); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM subscriptions`).Scan(&st.Subscriptions); err != nil {
		return st, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM matches`).Scan(&st.Matches); err != nil {
		return st, err
	}
	return st, nil
}

// GetMatchFans возвращает подписчиков любой из двух команд матча
// и помечает, какая сторона у пользователя любимая.
func (s *Storage) GetMatchFans(teamAID, teamBID int) ([]domain.MatchFan, error) {
	rows, err := s.db.Query(`
		SELECT user_id, team_id, favorite
		FROM subscriptions
		WHERE team_id IN (?, ?)
	`, teamAID, teamBID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	order := make([]int64, 0)
	byUser := make(map[int64]*domain.MatchFan)
	for rows.Next() {
		var userID int64
		var teamID, fav int
		if err := rows.Scan(&userID, &teamID, &fav); err != nil {
			return nil, err
		}
		fan := byUser[userID]
		if fan == nil {
			fan = &domain.MatchFan{UserID: userID}
			byUser[userID] = fan
			order = append(order, userID)
		}
		if teamID == teamAID {
			fan.FavoriteA = fan.FavoriteA || fav != 0
		}
		if teamID == teamBID {
			fan.FavoriteB = fan.FavoriteB || fav != 0
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fans := make([]domain.MatchFan, 0, len(order))
	for _, id := range order {
		fans = append(fans, *byUser[id])
	}
	return fans, nil
}

func (s *Storage) GetUserSubscriptions(userID int64) ([]domain.TeamInfo, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name, s.favorite, s.subscribed_at
		FROM subscriptions s
		INNER JOIN teams t ON t.id = s.team_id
		WHERE s.user_id = ?
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []domain.TeamInfo
	for rows.Next() {
		var id int
		var name string
		var fav int
		var at int64
		if err := rows.Scan(&id, &name, &fav, &at); err != nil {
			return nil, err
		}
		teams = append(teams, domain.TeamInfo{
			ID:           id,
			Name:         name,
			Favorite:     fav != 0,
			SubscribedAt: subscribedAtFromUnix(at),
		})
	}
	return teams, rows.Err()
}

func subscribedAtFromUnix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func (s *Storage) GetSubscribedTeamIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT team_id FROM subscriptions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, fmt.Sprintf("%d", id))
	}
	return ids, rows.Err()
}

func likeContains(query string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(query) + "%"
}

func (s *Storage) SearchTeams(query string) ([]domain.SearchedTeam, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name, GROUP_CONCAT(p.name, ', ')
		FROM teams t
		LEFT JOIN players p ON t.id = p.team_id
		WHERE t.name LIKE ? ESCAPE '\' COLLATE NOCASE
		GROUP BY t.id, t.name
		LIMIT 10
	`, likeContains(query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []domain.SearchedTeam
	for rows.Next() {
		var t domain.SearchedTeam
		var players sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &players); err != nil {
			return nil, err
		}
		if players.Valid {
			t.Players = players.String
		}
		teams = append(teams, t)
	}
	return teams, rows.Err()
}

func (s *Storage) GetTeamsByIDs(ids []int) ([]domain.TeamInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	args := make([]any, len(ids))
	placeholders := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		placeholders[i] = "?"
	}

	query := fmt.Sprintf("SELECT id, name FROM teams WHERE id IN (%s)", strings.Join(placeholders, ","))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var teams []domain.TeamInfo
	for rows.Next() {
		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		teams = append(teams, domain.TeamInfo{
			ID:   id,
			Name: name,
		})
	}
	return teams, rows.Err()
}
