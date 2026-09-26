package bot

import (
	"strconv"
	"strings"
)

// Границы сдвига в часах от UTC. Канонические границы БД лежат в storage
// (MinUTCOffset/MaxUTCOffset), здесь дубль для bot-слоя чтобы не тянуть
// импорт storage ради двух чисел.
const (
	minUTCOffset = -12
	maxUTCOffset = 14
)

// wantUTCHour переводит желаемый локальный час юзера в UTC-час срабатывания.
// Вся бизнес-логика в UTC, в пояс приводим только на рендере.
func wantUTCHour(hourLocal, utcOffset int) int {
	return ((hourLocal-utcOffset)%24 + 24) % 24
}

// wallHour переводит хранимый UTC-час в локальный wall-time юзера.
// Обратное преобразование — wantUTCHour. Storage хранит UTC,
// конвертация в обе стороны — задача bot-слоя.
func wallHour(hourUTC, utcOffset int) int {
	return ((hourUTC+utcOffset)%24 + 24) % 24
}

// parseUTCOffset разбирает "3", "+3", "-5", "+10" в сдвиг -12..+14.
func parseUTCOffset(raw string) (int, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "UTC")
	s = strings.TrimSuffix(s, "utc")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	s = strings.TrimPrefix(s, "UTC")
	s = strings.TrimPrefix(s, "utc")
	s = strings.TrimSpace(s)
	off, err := strconv.Atoi(s)
	if err != nil || off < minUTCOffset || off > maxUTCOffset {
		return 0, false
	}
	return off, true
}
