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
| Main score loop | `cmd/linkari/server_score.go:scoreAsync` | `ts-go extract cmd/linkari/server_score.go scoreAsync` |

## Intent Routing Layer (EPIC-154 → EPIC-161, committed e8b7f19)

| Concept | File:Symbol | Command |
|---------|-------------|---------|
| Router struct | `cmd/linkari/handler.go:Router` | `ts-go extract cmd/linkari/handler.go Router` |
| Share resolution result | `cmd/linkari/handler.go:ShareResolution` | `ts-go extract cmd/linkari/handler.go ShareResolution` |
| Resolve share → action | `cmd/linkari/handler.go:resolveShareAction` | `ts-go extract cmd/linkari/handler.go resolveShareAction` |
| Intent → profile | `cmd/linkari/intent.go:deriveProfileFromIntent` | `ts-go extract cmd/linkari/intent.go deriveProfileFromIntent` |
| Profile → intent | `cmd/linkari/intent.go:profileToIntentLookup` | `ts-go extract cmd/linkari/intent.go profileToIntentLookup` |
| Workflow registry | `cmd/linkari/intent_capture_registry.go:RegisterIntentCapture` | `ts-go extract cmd/linkari/intent_capture_registry.go RegisterIntentCapture` |
| Classify by intent metadata | `cmd/linkari/server_score.go:classifyByIntentMetadata` | `ts-go extract cmd/linkari/server_score.go classifyByIntentMetadata` |
| Intent stats | `cmd/linkari/stats_intent.go` | `ts-go funcs --format compact cmd/linkari/stats_intent.go` |

## Tags Layer (EPIC-149 → EPIC-153)

| Concept | File:Symbol | Command |
|---------|-------------|---------|
| Tags API handler | `cmd/linkari/server.go:handleGetTags` | `ts-go extract cmd/linkari/server.go handleGetTags` |
| Tag injection into prompt | `cmd/linkari/scoring_prompt_tags.go` | `ts-go funcs --format compact cmd/linkari/scoring_prompt_tags.go` |

## Fast Batch Orientation

```bash
# One-turn orientation for a new feature touching config + sources:
ts-go funcs --format compact cmd/linkari/config.go
ts-go funcs --format compact cmd/linkari/source.go
ts-go types --format compact cmd/linkari/config.go
```

Then extract specific targets in the next turn.
