package bot

import (
	"fmt"
	"strings"
	"time"
)

// tgChunkLimit — запас под лимит Telegram в 4096 символов на сообщение.
const tgChunkLimit = 3500

func formatTGTime(t time.Time, tgFormat, fallbackLayout string, utcOffset int) string {
	local := t.UTC().Add(time.Duration(utcOffset) * time.Hour)
	fallback := fmt.Sprintf("%s UTC%+d", local.Format(fallbackLayout), utcOffset)
	return tgTimeTag(t, tgFormat, fallback)
}

func formatUpcomingMatchTime(start, now time.Time, utcOffset int) string {
	offset := time.Duration(utcOffset) * time.Hour
	localStart := start.UTC().Add(offset)
	year, month, day := localStart.Date()
	nowYear, nowMonth, nowDay := now.UTC().Add(offset).Date()

	tgFormat, layout := "dt", "02.01.2006 15:04"
	switch {
	case year == nowYear && month == nowMonth && day == nowDay:
		tgFormat, layout = "t", "15:04"
	case year == nowYear && month == nowMonth:
		layout = "2, 15:04"
	case year == nowYear:
		layout = "02.01 15:04"
	}
	return tgTimeTag(start, tgFormat, localStart.Format(layout))
}

func tgTimeTag(t time.Time, format, fallback string) string {
	return fmt.Sprintf(`<tg-time unix="%d" format="%s">%s</tg-time>`, t.Unix(), format, fallback)
}

// chunkLines режет текст на куски по границам строк, чтобы каждый влез в limit.
// Единственное место с логикой нарезки: нарезка очереди — внутри enqueueToUser,
// немедленная отправка — в sendChunked.
func chunkLines(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	for _, line := range strings.SplitAfter(text, "\n") {
		if cur.Len()+len(line) > limit {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(line)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}
