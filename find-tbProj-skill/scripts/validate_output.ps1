param(
  [Parameter(Mandatory = $true)]
  [string]$Path
)

if (-not (Test-Path -LiteralPath $Path)) {
  throw "Output file does not exist: $Path"
}

$data = Get-Content -LiteralPath $Path -Raw -Encoding UTF8 | ConvertFrom-Json
$projects = @($data.projects)
$ids = @($projects | ForEach-Object { $_.projectId })
$uniqueIds = @($ids | Sort-Object -Unique)
$validUrls = @($projects | Where-Object {
  $_.projectUrl -eq "https://www.teambition.com/project/$($_.projectId)/works/$($_.rootParentId)"
})

[pscustomobject]@{
  Path = (Resolve-Path -LiteralPath $Path).Path
  Processed = $data.processedCount
  Success = $data.successCount
  Failure = $data.failureCount
  ProjectRecords = $projects.Count
  UniqueProjectIds = $uniqueIds.Count
  ValidWorksUrls = $validUrls.Count
  FailureDetails = @($data.failures)
}

