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
	return fmt.Sprintf(`<tg-time unix="%d" format="%s">%s UTC%+d</tg-time>`,
		t.Unix(), tgFormat, local.Format(fallbackLayout), utcOffset)
}

// chunkLines режет текст на куски по границам строк, чтобы каждый влез в limit.
// Строка длиннее limit режется сама. Пустые куски не возвращаются.
// Единственное место с логикой нарезки: нарезка очереди — внутри enqueueToUser,
// немедленная отправка — в sendChunked.
func chunkLines(text string, limit int) []string {
	if limit < 1 {
		limit = 1
	}
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		chunks = append(chunks, cur.String())
		cur.Reset()
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		for len(line) > limit {
			flush()
			chunks = append(chunks, line[:limit])
			line = line[limit:]
		}
		if line == "" {
			continue
		}
		if cur.Len()+len(line) > limit {
			flush()
		}
		cur.WriteString(line)
	}
	flush()
	if len(chunks) == 0 {
		return []string{text}
	}
	return chunks
}
