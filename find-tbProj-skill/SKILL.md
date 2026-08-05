---
name: find-tb-project-urls
description: Discover Teambition project file-library URLs from the signed-in "我参与的项目" page using the Codex in-app browser. Use this skill whenever the user asks to find, list, continue, resume, refresh, or export Teambition/TB project file URLs, works URLs, or the projects they participate in. It supports a supplied organization URL or the currently open Teambition page, incremental JSON persistence, resume-after-interruption, deduplication, bounded retries, and manual authentication recovery. It does not crawl files or folder contents and does not use the official SDK.
compatibility: Codex Desktop with the in-app browser skill and filesystem write access.
---

# Teambition Project URL Discovery

Use the existing signed-in Teambition session in the Codex in-app browser to build a durable inventory of project file-library roots. The purpose is discovery only: do not enumerate files, download attachments, or call the official Teambition SDK.

## Inputs

Accept either:

- an organization page URL, normally ending in `/organization/{organizationId}/my`; or
- the currently open Teambition organization page when no URL is supplied.

Optional arguments:

- `--output <path>`: output JSON path. Default: `D:\桌面\suosi-export\tb_discovered_projects.json`.
- `--limit <n>`: maximum new projects for a controlled test. Use `10` for acceptance tests; omit for full discovery.
- `--retry <n>`: retry count for transient failures. Default and maximum: `2`.

If the page requires sign-in, MFA, a captcha, or an approval dialog, pause and ask the user to complete it in the same in-app browser. Continue only after the user confirms that the page is ready. Never inspect or extract cookies, passwords, local storage, or session files.

## Browser workflow

1. Connect to the existing in-app browser binding and claim the existing Teambition tab. Reuse one tab for the whole run; do not close the browser or open a fresh login session for each project.
2. Navigate to the supplied organization URL if one was supplied. Otherwise use the current page. Verify that the page is the user's project portal and that the visible entry is `我参与的项目`.
3. Collect project cards using the project-card selector:

   ```css
   [data-goldlog-key="project-portal.project-card"]
   ```

   Read the visible project name from `.name__wt7q` when available. Deduplicate cards by the project ID extracted from their link or navigation target.
4. The project list is an inner scroll container, not the page body. Scroll that container with the browser CUA (for example `tab.cua.scroll({x:400,y:620,scrollY:520,scrollX:0})`) and recollect cards after each scroll. Stop discovery only after three consecutive scrolls produce no new project ID. If the portal exposes a project total, use it as a cross-check, not as the sole completion signal.
5. For each newly discovered project, click its actual card. Do not synthesize a `/works` URL from the project ID, and do not treat the initial `/tasks` URL as a result.
6. Wait for the project page to render. In the top project navigation, click the exact visible tab `文件` or `项目文件`. Either label is valid. A project may show `项目资料` instead; that is not the file-library tab.
7. Read the resulting URL. Accept only this shape:

   ```text
   https://www.teambition.com/project/{projectId}/works/{rootParentId}
   ```

   Extract `projectId` and `rootParentId` from the URL and verify that the project ID matches the card that was opened. A URL ending only in `/tasks` or `/tasks/view/...` is not success.
8. Return to the organization project list and continue using the same browser tab and session. Allow enough time for navigation and lazy rendering; a short wait after navigation and after clicking the file tab is expected.

## Missing tabs and errors

- If neither exact tab `文件` nor exact tab `项目文件` exists after the page has rendered, record a failure with `error: "no_file_library"` and continue. Do not infer a root ID.
- For timeouts, navigation failures, unexpected URLs, or transient page errors, retry the same project at most two times. If all attempts fail, record the concrete error and continue with other projects.
- If authentication becomes invalid, pause for manual user recovery as described above. Do not mark every remaining project failed while the user is signing in.
- A single failed project must never abort the whole discovery run.

## Incremental JSON and resume

Before scanning, load the output file if it exists. Validate that it is valid JSON, has a `projects` array, and that every successful record has a unique `projectId` and a URL matching the required `/project/{id}/works/{root}` shape. If the file is malformed, stop without overwriting it and report the path and validation error.

Skip any project whose `projectId` already appears in `projects`, even if its name is different. Existing successful records are authoritative for now. Process new projects and recorded failures that are eligible for retry.

After every project attempt, update and atomically rewrite the JSON (write a temporary sibling file, flush/close it, then replace the target). This is why an interrupted run can resume without repeating completed work. Never replace the user's formal output with a test output.

Use this schema:

```json
{
  "generatedAt": "ISO-8601 timestamp",
  "organizationUrl": "https://www.teambition.com/organization/{id}/my",
  "processedCount": 0,
  "successCount": 0,
  "failureCount": 0,
  "newCount": 0,
  "skippedCount": 0,
  "retryCount": 0,
  "projects": [
    {
      "projectId": "...",
      "projectName": "...",
      "projectUrl": "https://www.teambition.com/project/{projectId}/works/{rootParentId}",
      "rootParentId": "..."
    }
  ],
  "failures": [
    {
      "projectId": "...",
      "projectName": "...",
      "error": "no_file_library|timeout|unexpected_url|...",
      "attempts": 1
    }
  ],
  "totalProjectCount": 0
}
```

`processedCount` counts projects attempted in the current accumulated inventory, `successCount` counts records in `projects`, and `failureCount` counts current entries in `failures`. Keep failures so a later run can retry them. Set the final run status in the user-facing report to `success` when there are no failures, otherwise `partial`.

## Controlled test mode

For acceptance testing, use `--limit 10` and a temporary output path outside the formal JSON, such as `teambition-inventory-test\tb_discovered_projects.test.json`. Verify that:

- at most 10 new projects are attempted;
- each successful URL has the required `/works/{rootParentId}` form;
- project IDs are unique;
- the JSON can be read again after every write; and
- `D:\桌面\suosi-export\tb_discovered_projects.json` has the same file size and hash before and after the test.

Do not run a full 153-project test merely to validate the Skill.

## Final report

Report the output path, total discovered cards, successful URLs, failures, retries, skipped existing projects, and final status. Explicitly distinguish “no file-library tab” from an authentication or network failure. Do not claim success based only on a browser page refresh; success requires a persisted, re-readable JSON record.

