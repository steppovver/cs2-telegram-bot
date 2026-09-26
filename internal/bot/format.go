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

func splitDigestText(text string) []string {
	return chunkLines(text, tgChunkLimit)
}

// chunkLines режет текст на куски по границам строк, чтобы каждый влез в limit.
// Единственное место с логикой нарезки: splitDigestText и sendChunked —
// тонкие обертки над ней.
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
