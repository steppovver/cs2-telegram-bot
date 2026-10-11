package bot

import (
	"context"
	"log/slog"

	"cs2bot/internal/youtube"
)

// SetYouTubeResolver задает воркер резолва имен YouTube-каналов. nil = выключен.
func (b *Bot) SetYouTubeResolver(r *youtube.Resolver) {
	b.youtubeResolver = r
}

// StartYouTubeResolver запускает фоновый oEmbed-резолвер имен каналов.
func (b *Bot) StartYouTubeResolver(ctx context.Context) {
	if b.youtubeResolver == nil {
		slog.Info("Резолвер YouTube-каналов выключен")
		return
	}
	b.youtubeResolver.Start(ctx)
}
