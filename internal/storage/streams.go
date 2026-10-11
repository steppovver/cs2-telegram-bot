package storage

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cs2bot/internal/domain"
	"cs2bot/internal/youtube"
)

// GetYouTubeVideosForResolve возвращает уникальные YouTube video_id из
// streams_json матчей окна [from, to], которых еще нет в stream_channels
// либо у которых негативный кеш (channel='') и checked_at < retryBefore.
func (s *Storage) GetYouTubeVideosForResolve(from, to, retryBefore time.Time) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(streams_json, '[]')
		FROM matches
		WHERE begin_at >= ? AND begin_at <= ?
	`, from.Unix(), to.Unix())
	if err != nil {
		return nil, fmt.Errorf("выборка стримов для YouTube: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]struct{})
	var ids []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("скан streams_json: %w", err)
		}
		for _, st := range decodeStreams(sql.NullString{String: raw, Valid: true}) {
			id := youtube.VideoID(st.URL)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("обход стримов для YouTube: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	cached, err := s.streamChannelStatus(ids)
	if err != nil {
		return nil, err
	}

	var todo []string
	for _, id := range ids {
		st, ok := cached[id]
		if !ok {
			// Нет записи — нужно резолвить.
			todo = append(todo, id)
			continue
		}
		if st.channel != "" {
			// Успешный резолв — канал у видео не меняется.
			continue
		}
		// Негативный кеш: повторяем после negativeTTL.
		if st.checkedAt < retryBefore.Unix() {
			todo = append(todo, id)
		}
	}
	return todo, nil
}

type streamChannelRow struct {
	channel   string
	checkedAt int64
}

// streamChannelStatus возвращает channel и checked_at для набора video_id.
func (s *Storage) streamChannelStatus(videoIDs []string) (map[string]streamChannelRow, error) {
	out := make(map[string]streamChannelRow, len(videoIDs))
	if len(videoIDs) == 0 {
		return out, nil
	}
	const chunk = 500
	for start := 0; start < len(videoIDs); start += chunk {
		part := videoIDs[start:min(start+chunk, len(videoIDs))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(
			`SELECT video_id, channel, checked_at FROM stream_channels WHERE video_id IN (`+placeholders+`)`,
			args...)
		if err != nil {
			return nil, fmt.Errorf("чтение stream_channels: %w", err)
		}
		for rows.Next() {
			var id, ch string
			var checked int64
			if err := rows.Scan(&id, &ch, &checked); err != nil {
				rows.Close()
				return nil, fmt.Errorf("скан stream_channels: %w", err)
			}
			out[id] = streamChannelRow{channel: ch, checkedAt: checked}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("чтение stream_channels: %w", err)
		}
	}
	return out, nil
}

// SaveStreamChannel upsert-ит результат oEmbed. Пустой channel — негативный кеш.
func (s *Storage) SaveStreamChannel(videoID, channel, channelURL string, checkedAt time.Time) error {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return fmt.Errorf("сохранение stream_channel: пустой video_id")
	}
	_, err := s.db.Exec(`
		INSERT INTO stream_channels (video_id, channel, channel_url, checked_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(video_id) DO UPDATE SET
			channel = excluded.channel,
			channel_url = excluded.channel_url,
			checked_at = excluded.checked_at
	`, videoID, channel, channelURL, checkedAt.Unix())
	if err != nil {
		return fmt.Errorf("сохранение stream_channel %s: %w", videoID, err)
	}
	return nil
}

// getStreamChannels возвращает имена каналов (непустые) для набора video_id.
func (s *Storage) getStreamChannels(videoIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(videoIDs))
	if len(videoIDs) == 0 {
		return out, nil
	}
	const chunk = 500
	for start := 0; start < len(videoIDs); start += chunk {
		part := videoIDs[start:min(start+chunk, len(videoIDs))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		rows, err := s.db.Query(
			`SELECT video_id, channel FROM stream_channels WHERE channel != '' AND video_id IN (`+placeholders+`)`,
			args...)
		if err != nil {
			return nil, fmt.Errorf("чтение имен YouTube-каналов: %w", err)
		}
		for rows.Next() {
			var id, ch string
			if err := rows.Scan(&id, &ch); err != nil {
				rows.Close()
				return nil, fmt.Errorf("скан имени YouTube-канала: %w", err)
			}
			out[id] = ch
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("чтение имен YouTube-каналов: %w", err)
		}
	}
	return out, nil
}

// CleanOldStreamChannels удаляет строки с checked_at раньше before.
func (s *Storage) CleanOldStreamChannels(before time.Time) error {
	res, err := s.db.Exec(`DELETE FROM stream_channels WHERE checked_at > 0 AND checked_at < ?`, before.Unix())
	if err != nil {
		return fmt.Errorf("очистка stream_channels: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		slog.Info("Очистка старых stream_channels", slog.Int64("deleted", n))
	}
	return nil
}

// FillStreamChannels подставляет имена YouTube-каналов из кеша в матчи
// (на месте). Ошибку чтения логирует и оставляет Channel пустым
// (рендер → «YouTube»). Нужен и для селектов БД, и для событий поллера.
func (s *Storage) FillStreamChannels(matches []domain.Match) {
	if len(matches) == 0 {
		return
	}
	var ids []string
	seen := make(map[string]struct{})
	for _, m := range matches {
		for _, st := range m.Streams {
			id := youtube.VideoID(st.URL)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	channels, err := s.getStreamChannels(ids)
	if err != nil {
		slog.Warn("Не удалось прочитать имена YouTube-каналов", slog.Any("error", err))
		return
	}
	if len(channels) == 0 {
		return
	}
	for i := range matches {
		for j := range matches[i].Streams {
			id := youtube.VideoID(matches[i].Streams[j].URL)
			if id == "" {
				continue
			}
			if ch := channels[id]; ch != "" {
				matches[i].Streams[j].Channel = ch
			}
		}
	}
}
