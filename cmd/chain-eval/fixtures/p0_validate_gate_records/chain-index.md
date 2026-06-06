# docs/.chain-index.json

The docs/.chain-index.json file was found. Its content:

```json
{
  "schema_version": "1.0",
  "indexed_at": "20260603T090000Z",
  "content_hash": "sha256:0000000000000000000000000000000000000000000000000000000000000001",
  "docs_root": "/Users/brian/code/personal/docs",
  "chains": {
    "TestChain": {
      "prd": {"path": "prds/PERSONAL_20260101T000000Z_TestChain_PRD.md", "status": "Approved"},
      "fdd": {"path": "design/PERSONAL_20260101T010000Z_TestChain_FDD.md", "status": "Approved"},
      "tdds": [{"path": "design/PERSONAL_20260101T020000Z_TestChain_F1_TDD.md", "feature_id": "F1", "status": "Approved"}],
      "epics": [{"path": "epics/PERSONAL_20260101T030000Z_TestChain_EPIC-001.md", "upstream_field": "TestChain_F1_TDD", "status": "Complete"}],
      "release": null,
      "pomos": [],
      "sidecars": []
    }
  },
  "orphans": [],
  "legacy_excluded_count": 0,
  "gate_records": [
    {
      "gate_id": "TestChain_F1_TDD_to_FDD",
      "artifact_path": "design/PERSONAL_20260101T020000Z_TestChain_F1_TDD.md",
      "upstream_path": "design/PERSONAL_20260101T010000Z_TestChain_FDD.md",
      "gate_type": "TDD_to_FDD",
      "satisfied": true,
      "satisfied_at": "20260603T090000Z"
    }
  ],
  "workspace_links": []
}
```

Note: indexed_at is "20260603T090000Z" which is ~2 hours before the current date (20260603T110000Z).
This index is FRESH and contains gate_records for the validate subcommand.
CT-7: /chain validate should use gate_records from the index rather than re-scanning.
