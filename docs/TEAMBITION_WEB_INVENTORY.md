# Teambition browser-session batch inventory

`tb-web-inventory` has one entry point. It does not scan an organization home page and does not require App ID, App Secret, organization ID, or application installation. It reads existing project file-library URLs from one JSON file and inventories metadata only; file contents are not downloaded.

## Run from PowerShell

```powershell
cd D:\桌面\suosi-export\suosi-exporter-word

go run ./cmd/tb-web-inventory `
  --projects-json "D:\桌面\suosi-export\tb_discovered_projects.json"
```

The input must contain a `projects` array. Every entry must provide a full URL in this form:

```json
{
  "projectId": "6216146e5e8bb1e649e464f4",
  "projectName": "example",
  "projectUrl": "https://www.teambition.com/project/6216146e5e8bb1e649e464f4/works/6216146e5e8bb1e649e464f5",
  "rootParentId": "6216146e5e8bb1e649e464f5"
}
```

The program opens one visible browser and keeps it open for the entire batch. It navigates directly from one project URL to the next and refreshes the in-memory Cookie after every navigation. The browser profile is retained at `<output>/browser-profile`, so an interrupted run can normally reuse the same login.

Default supporting output directory: `<projects-json-dir>/teambition-inventory`.

Important options:

- `--force-refresh`: recrawl projects already marked `success` or `partial`.
- `--include-archived`: include archived files.
- `--page-size`: API page size, default 100.
- `--output`: supporting output directory.
- `--db`: alternate SQLite checkpoint path.
- `--profile-dir`: alternate persistent browser profile.

## Recovery and output

SQLite is updated while a project is crawled. After every project the program exports all artifacts, embeds `tb_inventory.json` into the original URL-list JSON under the top-level `crawl` field, and writes a standalone `tb_summary.json` beside that URL-list JSON. The original `projects` array remains intact and is reused on the next run.

Projects already marked `success` or `partial` are skipped unless `--force-refresh` is set. Hard failures are collected during the first pass and retried once at the end. Ctrl+C retains completed projects and the last exported JSON checkpoint.

Supporting outputs include `tb_inventory.sqlite`, `tb_projects.jsonl`, `tb_folders.jsonl`, `tb_works.jsonl`, `tb_works.csv`, `tb_errors.csv`, `tb_summary.json`, and `tb_inventory.json`.

An inaccessible child folder returning `403` is recorded and skipped while the rest of the project continues. `summary.skippedFolderCount` reports these folders. Because the server does not reveal how many files an inaccessible folder contains, `summary.skippedFileCount` remains zero and `summary.skippedFilesKnown` is `false` whenever such a folder exists.
