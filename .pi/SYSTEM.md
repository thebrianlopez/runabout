# Runabout Critical File Index

Fast path to common implementation targets. Use these with `ts-go extract`.

## Config & Registration

| Concept | File:Symbol | Command |
|---------|-------------|---------|
| Server config | `cmd/linkari/config.go:ServerConfig` | `ts-go extract cmd/linkari/config.go ServerConfig` |
| Push config | `cmd/linkari/config.go:PushConfig` | `ts-go extract cmd/linkari/config.go PushConfig` |
| Source registry | `cmd/linkari/source.go:registeredSources` | `ts-go extract cmd/linkari/source.go registeredSources` |
| Source interface | `cmd/linkari/source.go:ContentSource` | `ts-go extract cmd/linkari/source.go ContentSource` |

## YouTube Pipeline

| Concept | File:Symbol | Command |
|---------|-------------|---------|
| Subscriptions source | `cmd/linkari/youtube_subs.go:YouTubeSubsSource` | `ts-go extract cmd/linkari/youtube_subs.go YouTubeSubsSource` |
| Watch Later source | `cmd/linkari/youtube_wl.go:YouTubeWatchLaterSource` | `ts-go extract cmd/linkari/youtube_wl.go YouTubeWatchLaterSource` |
| Liked videos source | `cmd/linkari/youtube_likes.go:YouTubeLikedSource` | `ts-go extract cmd/linkari/youtube_likes.go YouTubeLikedSource` |
| Subscriptions poll | `cmd/linkari/youtube_subs.go:watchSubscriptionsAsync` | `ts-go extract cmd/linkari/youtube_subs.go watchSubscriptionsAsync` |
| Watch Later sync | `cmd/linkari/youtube_wl.go:syncWatchLaterAsync` | `ts-go extract cmd/linkari/youtube_wl.go syncWatchLaterAsync` |

## Queue & Scoring

| Concept | File:Symbol | Command |
|---------|-------------|---------|
| Enqueue | `cmd/linkari/queue.go:Queue.Enqueue` | `ts-go extract cmd/linkari/queue.go Enqueue` |
| Deduplication | `cmd/linkari/queue.go:Queue.IsNewContent` | `ts-go extract cmd/linkari/queue.go IsNewContent` |
| Scoring | `cmd/linkari/server_score.go` | `ts-go funcs --format compact cmd/linkari/server_score.go` |

## Fast Batch Orientation

```bash
# One-turn orientation for a new feature touching config + sources:
ts-go funcs --format compact cmd/linkari/config.go
ts-go funcs --format compact cmd/linkari/source.go
ts-go types --format compact cmd/linkari/config.go
```

Then extract specific targets in the next turn.
