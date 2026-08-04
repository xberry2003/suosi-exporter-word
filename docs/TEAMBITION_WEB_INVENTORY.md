# Teambition browser-session inventory

`tb-web-inventory` inventories project file metadata using the same login pattern as the original Suosi crawler: it opens a temporary browser, waits for Teambition login, then calls the web endpoints with that browser session.

It does not require a Teambition App ID, App Secret, organization ID, or application installation. The existing `tb-inventory` SDK command is unchanged.

## Doctor

Run this first from PowerShell:

```powershell
cd D:\桌面\suosi-exporter\modified_thoughtsexport

$projectUrl = 'https://www.teambition.com/project/6216146e5e8bb1e649e464f4/works/6216146e5e8bb1e649e464f5'

go run ./cmd/tb-web-inventory doctor `
  --project-url $projectUrl
```

A temporary Chrome or Edge window opens. Log in in that window if prompted. The window closes after the session is detected. A successful result looks like:

```text
doctor ok: project=... parent=... folders=N files=N (metadata only)
```

Use `--raw-response` only for diagnostics. It prints file metadata returned by Teambition, but never prints the browser Cookie.

## Inventory

```powershell
go run ./cmd/tb-web-inventory inventory `
  --project-url $projectUrl `
  --output ./output/teambition-web `
  --force-refresh
```

The URL segment after `/works/` is automatically used as the starting root folder. `--parent-id` can override it when needed.

Default output directory: `./output/teambition-web`

Files produced:

- `tb_inventory.sqlite`: resumable SQLite inventory database.
- `tb_projects.jsonl`: project records.
- `tb_folders.jsonl`: folder records.
- `tb_works.jsonl`: file metadata records.
- `tb_works.csv`: file metadata for spreadsheet use.
- `import_resources.jsonl`: downstream import queue format.
- `tb_summary.json` and `tb_summary.md`: totals and breakdowns.
- `tb_errors.csv`: crawl errors.

This program inventories metadata and does not download file contents. Browser cookies are kept in process memory and are not written to output files or logs.

If an individual child folder returns HTTP or business code `403`, browser mode records it in `tb_errors.csv`, skips that folder, and continues crawling the remaining folders. The final command status is `partial`; inaccessible folder contents are not included, while all other reachable metadata is exported normally.

## Notes

The web endpoints are undocumented Teambition interfaces and may change. If Teambition changes their response format, run `doctor --raw-response` and update the client parser. The temporary browser profile is deleted after each run, so a new run may require login again.
