# Teambition task probe

The existing `cmd/tb-inventory` CLI includes a read-only MCP probe. It uses `TEAMBITION_MCP_HOST` and `TEAMBITION_MCP_TOKEN` only; it does not use App ID, App Secret, JWT, or any TeamFlow API.

Validate environment variables without printing the token:

```powershell
$h = [Environment]::GetEnvironmentVariable('TEAMBITION_MCP_HOST')
$t = [Environment]::GetEnvironmentVariable('TEAMBITION_MCP_TOKEN')
'host-present=' + (![string]::IsNullOrWhiteSpace($h))
'token-present=' + (![string]::IsNullOrWhiteSpace($t)) + ' token-length=' + $(if ($null -eq $t) { 0 } else { $t.Length })
```

Probe one project/task:

```powershell
go run ./cmd/tb-inventory tasks probe `
  --project "https://www.teambition.com/project/6216146e5e8bb1e649e464f4/tasks/view/6216146ea18db7003fe55754" `
  --task "https://www.teambition.com/project/6216146e5e8bb1e649e464f4/tasks/view/6216146ea18db7003fe55754/task/630f3535eed39a003f7a6469" `
  --output ./exports `
  --resume
```

Results are resumable under `exports/teambition/task-probe/{projectId}/{taskId}/`: `raw/` stores successful MCP responses, `state.json` records the last completed call, and `coverage-report.json`/`.md` record status, counts, key-field coverage, and failures. The probe does not create or update Teambition content and does not write TeamFlow records.
