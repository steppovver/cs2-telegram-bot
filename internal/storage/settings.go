package storage

import (
	"database/sql"
	"fmt"
	"strings"

	"cs2bot/internal/domain"
)

const (
	// Дефолт 10:00 при UTC+3 == 07:00 UTC. Колонка hour хранит UTC.
	defaultDigestHourUTC = 7
	minDigestHour        = 0
	maxDigestHour        = 23
)

const (
	DefaultUTCOffset = 3
	MinUTCOffset     = -12
	MaxUTCOffset     = 14
)

func NormalizeUTCOffset(o int) int {
	if o < MinUTCOffset || o > MaxUTCOffset {
		return DefaultUTCOffset
	}
	return o
}

// withUser открывает транзакцию, гарантирует строку в users и вызывает fn.
func (s *Storage) withUser(userID int64, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`INSERT OR IGNORE INTO users (id) VALUES (?)`, userID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) GetUserOffset(userID int64) (int, error) {
	var off int
	err := s.db.QueryRow(`SELECT utc_offset FROM user_settings WHERE user_id = ?`, userID).Scan(&off)
	if err == sql.ErrNoRows {
		return DefaultUTCOffset, nil
	}
	if err != nil {
		return DefaultUTCOffset, err
	}
	return NormalizeUTCOffset(off), nil
}

func (s *Storage) SetUserOffset(userID int64, offset int) error {
	if offset < MinUTCOffset || offset > MaxUTCOffset {
		return fmt.Errorf("сдвиг должен быть от %d до %+d", MinUTCOffset, MaxUTCOffset)
	}
	return s.withUser(userID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO user_settings (user_id, utc_offset)
			VALUES (?, ?)
			ON CONFLICT(user_id) DO UPDATE SET utc_offset = excluded.utc_offset`,
			userID, offset)
		return err
	})
}

func (s *Storage) GetUserOffsets(userIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(userIDs))
	for _, id := range userIDs {
		out[id] = DefaultUTCOffset
	}
	if len(userIDs) == 0 {
		return out, nil
	}
	args := make([]any, len(userIDs))
	placeholders := make([]string, len(userIDs))
	for i, id := range userIDs {
		args[i] = id
		placeholders[i] = "?"
	}
	query := fmt.Sprintf("SELECT user_id, utc_offset FROM user_settings WHERE user_id IN (%s)",
		strings.Join(placeholders, ","))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var off int
		if err := rows.Scan(&id, &off); err != nil {
			continue
		}
		out[id] = NormalizeUTCOffset(off)
	}
	return out, rows.Err()
}

func (s *Storage) GetDigestSettings(userID int64) (domain.DigestSettings, error) {
	// HourUTC отдаем как есть из БД, конвертация в wall-time — задача bot-слоя.
	var enabled int
	var hourUTC int
	err := s.db.QueryRow(`SELECT enabled, hour FROM user_digest WHERE user_id = ?`, userID).Scan(&enabled, &hourUTC)
	if err == sql.ErrNoRows {
		off, _ := s.GetUserOffset(userID)
		return domain.DigestSettings{Enabled: false, HourUTC: defaultDigestHourUTC, UtcOffset: off}, nil
	}
	if err != nil {
		return domain.DigestSettings{}, err
	}
	off, _ := s.GetUserOffset(userID)
	return domain.DigestSettings{Enabled: enabled != 0, HourUTC: hourUTC, UtcOffset: off}, nil
}

func (s *Storage) SetDigestEnabled(userID int64, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	return s.withUser(userID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO user_digest (user_id, enabled, hour)
			VALUES (?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET enabled = excluded.enabled`,
			userID, enabledInt, defaultDigestHourUTC)
		return err
	})
}

// SetDigestHour пишет час в UTC (конвертация wall->UTC — задача bot-слоя).
func (s *Storage) SetDigestHour(userID int64, hourUTC int) error {
	if hourUTC < minDigestHour || hourUTC > maxDigestHour {
		return fmt.Errorf("час должен быть от %d до %d", minDigestHour, maxDigestHour)
	}
	return s.withUser(userID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO user_digest (user_id, enabled, hour)
			VALUES (?, ?, ?)
			ON CONFLICT(user_id) DO UPDATE SET hour = excluded.hour`,
			userID, 0, hourUTC)
		return err
	})
}

func (s *Storage) GetDigestDueUsers(hourUTC int, slot string) ([]domain.DigestDueUser, error) {
	rows, err := s.db.Query(`SELECT d.user_id, d.hour, d.last_sent_date,
		COALESCE(st.utc_offset, ?) AS utc_offset
		FROM user_digest d
		LEFT JOIN user_settings st ON st.user_id = d.user_id
		WHERE d.enabled = 1 AND d.hour = ? AND d.last_sent_date != ?`,
		DefaultUTCOffset, hourUTC, slot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []domain.DigestDueUser
	for rows.Next() {
		var u domain.DigestDueUser
		var lastSent string
		if err := rows.Scan(&u.UserID, &u.HourUTC, &lastSent, &u.UtcOffset); err != nil {
			return nil, err
		}
		u.LastSent = lastSent
		u.UtcOffset = NormalizeUTCOffset(u.UtcOffset)
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Storage) MarkDigestSent(userID int64, date string) error {
	_, err := s.db.Exec(`UPDATE user_digest SET last_sent_date = ? WHERE user_id = ?`, date, userID)
	return err
}
