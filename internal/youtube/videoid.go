package youtube

import (
	"net/url"
	"strings"
)

// VideoID извлекает идентификатор видео из URL YouTube.
// Поддерживает watch?v=, youtu.be/, embed/, live/, /v/.
// Не-YouTube и битые ссылки дают "".
func VideoID(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return ""
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")

	switch host {
	case "youtu.be":
		id := firstPathSeg(u.Path)
		return cleanVideoID(id)
	case "youtube.com", "youtube-nocookie.com":
		if v := strings.TrimSpace(u.Query().Get("v")); v != "" {
			return cleanVideoID(v)
		}
		segs := pathSegs(u.Path)
		if len(segs) >= 2 {
			switch strings.ToLower(segs[0]) {
			case "embed", "live", "v", "shorts":
				return cleanVideoID(segs[1])
			}
		}
		return ""
	default:
		return ""
	}
}

func pathSegs(path string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(path, "/"), "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstPathSeg(path string) string {
	segs := pathSegs(path)
	if len(segs) == 0 {
		return ""
	}
	return segs[0]
}

// cleanVideoID отбрасывает хвосты вроде &t= и проверяет базовую длину.
// Стандартный ID YouTube — 11 символов [A-Za-z0-9_-].
func cleanVideoID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.IndexAny(id, "?&#"); i >= 0 {
		id = id[:i]
	}
	if len(id) < 6 || len(id) > 20 {
		return ""
	}
	for _, r := range id {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return ""
	}
	return id
}
