# Teambition project inventory

`cmd/tb-inventory` is an independent metadata inventory program in the existing 所思 exporter module. It does not import TeamFlow packages or open a TeamFlow database. TeamFlow can later consume `import_resources.jsonl`.

## API and identity

The program uses `github.com/teambition/openapi-sdk-golang v0.0.2`. Project discovery uses the SDK's paginated `ProjectAPI.ListUserProjectsV3` and `ProjectAPI.QueryProjectsV3`. File inventory uses paginated `FileAPI.ListFilesV3` with `XTenantId`, `ProjectId`, `DisplayPrefixPath(true)`, `IncludeArchived`, `PageSize`, optional `ParentId`, and `PageToken`. The SDK's `fileSize` is int32; the adapter safely stores it as int64. A file's stable identity is `project_id + work_id`; `resource_id` is `work:<workId>` and `sourceKey` is `workId`.

## Traversal and recovery

Pagination is handled inside each parent-folder request. Folder traversal is a separate queue, with visited folder and work sets to prevent cycles and duplicate pages. 401/403/404 are reported distinctly and are not treated as transient; 408/429/5xx and network errors receive bounded exponential backoff. SQLite is written as each page is processed, so Ctrl+C retains completed work. `--resume` is project-level recovery; `--force-refresh` ignores it. A partial crawl exits 2.

For a file-library URL such as `/project/{projectId}/works/{collectionId}`, `--project-url` extracts both IDs and starts traversal with `ParentId(collectionId)`. `--parent-id` can override the starting collection for a single explicitly selected project. This does not change work identity: actual file records still use the SDK work `id` as `workId`.

## Storage and outputs

The independent fact database is `output/teambition/tb_inventory.sqlite`, with `tb_crawl_runs`, `tb_projects`, `tb_folders`, `tb_works`, and `tb_crawl_errors`. Exports include `tb_works.jsonl`, `tb_works.csv`, `tb_projects.jsonl`, `tb_folders.jsonl`, `tb_summary.json`, `tb_summary.md`, `tb_errors.csv`, and `import_resources.jsonl`. JSON is UTF-8 and does not HTML-escape Chinese text. The current version inventories current API metadata only: it does not download files or count historical versions.

Common failures are 401 (invalid app authentication), 403 (missing organization/project permission), 404 (resource absent or invisible), and 429 (rate limiting, retried with backoff). Discovery failure never fabricates an all-projects result; explicit project IDs continue.

## PowerShell

```powershell
$env:TB_APP_ID = "..."
$env:TB_APP_SECRET = "..."
$env:TB_ORG_ID = "..."
$env:TB_OPERATOR_ID = "..." # optional
go run ./cmd/tb-inventory doctor
go run ./cmd/tb-inventory doctor --project-url "https://www.teambition.com/project/6216146e5e8bb1e649e464f4/works/6216146e5e8bb1e649e464f5" --raw-response
go run ./cmd/tb-inventory inventory --project-id 6216146e5e8bb1e649e464f4 --output ./output/teambition
go run ./cmd/tb-inventory inventory --project-url "https://www.teambition.com/project/6216146e5e8bb1e649e464f4/works/6216146e5e8bb1e649e464f5" --output ./output/teambition
go run ./cmd/tb-inventory summary --db ./output/teambition/tb_inventory.sqlite
go run ./cmd/tb-inventory export --db ./output/teambition/tb_inventory.sqlite --output ./output/teambition
```

Doctor reports HTTP status, Teambition business `code`, `errorMessage`, `requestId`, starting parent, next-page token and item counts. `--raw-response` additionally prints the first response body (truncated to 8 KiB). HTTP 200 is not considered success when the response carries a non-success business code or a non-empty `errorMessage`.

In the next phase TeamFlow should UPSERT by `(sourceSystem, resourceType, sourceProjectKey, sourceKey)`. After creating a target library/document, its importer can fill `targetLibraryId` and `targetDocId`, then change `status` from `pending` to `resolved`. The inventory remains read-only toward TeamFlow.

Historical-version storage requires the file-version API. Procurement must include replicas, backups, databases, logs and growth headroom, not only `raw1xBytes`.
