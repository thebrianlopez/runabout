---
schema_version: 1
task: PERSONAL_20260516T161503Z_Linkari_EPIC-113_PDF_Share_Pipeline_Action_Picker_LiteParse_Health_Confidence_Extraction_Profile_Routing
agent: runabout-agent
epic_path: /Users/brian/code/personal/docs/epics/PERSONAL_20260516T161503Z_Linkari_EPIC-113_PDF_Share_Pipeline_Action_Picker_LiteParse_Health_Confidence_Extraction_Profile_Routing.md
dispatched_at: 20260516T161558Z
status: complete
claimed_at: 20260516T000000Z
completed_at: 20260516T000000Z
milestones: [M3,M4,M5,M6,M7,M8,M9,M10,M11]
capabilities: []
producer: epic-dispatch
model: sonnet
---

# Task: PERSONAL_20260516T161503Z_Linkari_EPIC-113_PDF_Share_Pipeline_Action_Picker_LiteParse_Health_Confidence_Extraction_Profile_Routing.md

Execute milestones [M3,M4,M5,M6,M7,M8,M9,M10,M11] for this epic.

## Response

Runabout milestones M3-M11 verified against current local main. Backend PDF pipeline implementation is already present in commit `fa3fabea feat(pdf): PDF share pipeline — EPIC-102/103/104/105/107`.

Verified coverage by milestone:

- M3/M4 LiteParse health + graceful degradation: `cmd/linkari/liteparse_health_test.go`, `cmd/linkari/health_probe.go`, `cmd/linkari/cmd_doctor.go`, PDF failure path sets `content_warning=lit_parse_failed` and `extraction_confidence=0.0` without crashing.
- M5 HTTP multipart integration: `cmd/linkari/pdf_multipart_test.go` covers PDF multipart intake, metadata, queue persistence, and dedup path.
- M6/M7 confidence extraction: `cmd/linkari/liteparse_confidence_test.go`, `parseLiteParseJSON`, `runLiteParse` use `lit parse --format json`, mean page confidence, OCR retry, fallback semantics, and queue persistence.
- M8/M9 PDF profile routing: `cmd/linkari/pdf_profile_routing_test.go`, `classifyByIntentMetadata` routes `application/pdf` + finance category to `finance`, unknown falls through.
- M10 ContentTypePDF preamble: `ContentTypePDF` preamble is injected for document/PDF scoring and tested.
- M11 behavioral verification: empty/failure PDF cases are covered by LiteParse health/confidence and server score tests; targeted PDF test suite passes.

Validation run:

```bash
go test ./cmd/linkari -run 'Test.*(LiteParse|Confidence|PDF|Pdf|ProfileRouting|Multipart|Doctor)' -count=1
# ok   github.com/blo-grindr/runabout/cmd/linkari  7.547s

go test ./cmd/linkari -run 'TestCT|TestBT|TestRG|TestHandleShare_PDF|TestScoreAsync_Document|TestScoreAsync_PDF|TestDoctor|TestDoctorTessdata' -count=1 -coverprofile=/tmp/linkari_pdf_cover.out
# ok   github.com/blo-grindr/runabout/cmd/linkari  8.943s
```

Full `go test ./cmd/linkari` was attempted and failed on an unrelated existing flaky/racy image metadata test: `TestScoreAsync_ImageFileMetadataOnly`, where async scoring raced with test DB close (`ScoreByID ... sql: database is closed`). Targeted PDF/doctor/profile tests pass.

