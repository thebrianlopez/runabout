# docs/.chain-index.json

The docs/.chain-index.json file was found. Its content:

```json
{
  "schema_version": "1.0",
  "indexed_at": "20260603T090000Z",
  "content_hash": "sha256:0000000000000000000000000000000000000000000000000000000000000002",
  "docs_root": "/Users/brian/code/personal/docs",
  "chains": {
    "TestChain": {
      "prd": {"path": "prds/PERSONAL_20260101T000000Z_TestChain_PRD.md", "status": "Approved"},
      "fdd": {"path": "design/PERSONAL_20260101T010000Z_TestChain_FDD.md", "status": "Approved"},
      "tdds": [
        {"path": "design/PERSONAL_20260101T020000Z_TestChain_F1_TDD.md", "feature_id": "F1", "status": "Approved"},
        {"path": "design/PERSONAL_20260101T021000Z_TestChain_F2_TDD.md", "feature_id": "F2", "status": "Approved"},
        {"path": "design/PERSONAL_20260101T022000Z_TestChain_F3_TDD.md", "feature_id": "F3", "status": "In Review"}
      ],
      "epics": [
        {"path": "epics/PERSONAL_20260101T030000Z_TestChain_EPIC-001.md", "upstream_field": "TestChain_F1_TDD", "status": "Complete"},
        {"path": "epics/PERSONAL_20260101T031000Z_TestChain_EPIC-002.md", "upstream_field": "TestChain_F2_TDD", "status": "In Progress"}
      ],
      "release": null,
      "pomos": [],
      "sidecars": []
    }
  },
  "orphans": [],
  "legacy_excluded_count": 0,
  "gate_records": [],
  "workspace_links": []
}
```

Note: indexed_at is "20260603T090000Z" which is ~2 hours before the current date (20260603T110000Z).
This index is FRESH. CT-6 compares input_tokens for this fixture (index-backed) vs p0_index_absent
(full-scan) on the same corpus. Requires harness token tracking to verify >50% reduction.
