# Teambition collector contract

The tasks collect command creates a source-neutral collection package. It is an input to a future TeamFlow mapper/importer. The collector never calls TeamFlow, writes a TeamFlow database, or creates TeamFlow IDs.

## Command

    go run ./cmd/tb-inventory tasks collect \
      --project-url "https://www.teambition.com/project/PROJECT/tasks/view/VIEW" \
      --output ./exports \
      --include-raw \
      --download-assets=false \
      --concurrency 2

Use --resume after interruption and --since 2026-01-01T00:00:00Z for an updated-time lower bound. Authentication comes only from TEAMBITION_MCP_HOST and TEAMBITION_MCP_TOKEN.

## Package layout

The package root is output/teambition-collector/projectId. Entity files are UTF-8 JSON Lines. The raw directory is populated only when requested. The assets directory contains atomically downloaded files. checkpoints/checkpoint.json, manifest.json, errors.jsonl, and checksums.sha256 provide resume, audit, and verification.

Every entity uses schema version 1.0 and a source-stable Teambition external_id. Data fields use snake_case and contain no TeamFlow IDs. Unknown scalar values are null; known empty collections are empty arrays. Fingerprints cover normalized business data only and exclude fetch time and signed URLs.

Human comments detected from activity actions go to comments.jsonl. Other changes remain in activities.jsonl. System activity is never converted into a comment. Temporary signed URLs are not written to logs, manifest, fingerprints, or standard attachment entities.

## Coverage and versioning

MCP supplies projects, task groups, users, tasks, task details, activities/comments, progress, task links, status/workflow data, and file metadata. Tags without a dedicated readable MCP endpoint, hidden relations, browser-only counts, absent rich-text structures, and file version history remain partial or unavailable. Source data is never fabricated.

Additive schema changes are backward compatible within version 1.x. Removing fields or changing required-field meaning requires a new major schema version.
