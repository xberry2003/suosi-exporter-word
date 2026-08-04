# 所思导出与 Teambition 项目清单工具

这个仓库包含三部分能力：

- `go run .`：原有所思知识库导出工具，将 `thoughts.teambition.com` Workspace 导出为 `docx` 或 `html`。
- `go run ./cmd/tb-inventory`：Teambition 官方 OpenAPI SDK 路径，用 App ID / App Secret 抓取项目文件元数据。
- `go run ./cmd/tb-web-inventory`：旧爬虫浏览器会话路径，打开临时浏览器登录后，使用 Web 接口抓取项目文件元数据。

两个 Teambition 清单命令都只抓取文件、文件夹、项目和容量等元数据，不下载文件正文，也不写入 TeamFlow。

## 所思知识库导出

直接启动本地页面：

```bash
go run .
```

命令行导出：

```bash
go run . -url "https://thoughts.teambition.com/workspaces/xxxxxx/overview" -output exports -format docx
```

常用参数：

- `-url`：所思 Workspace 地址。
- `-output`：导出根目录，默认 `exports`。
- `-format`：导出格式，支持 `docx`、`html`。
- `-overwrite`：覆盖已有文件。
- `-retry-failed`：重新尝试失败项。
- `-dry-run`：只生成目录树预览，不下载。
- `-mock-data`：使用本地 mock JSON 调试。

导出过程会生成 `logs/`、`manifest.json`、`failed_docs.json` 等运行记录。

## Teambition 官方 SDK 路径

适用于已经有 Teambition OpenAPI 应用授权的场景。

先配置环境变量：

```powershell
$env:TB_APP_ID = "..."
$env:TB_APP_SECRET = "..."
$env:TB_ORG_ID = "..."
$env:TB_OPERATOR_ID = "..." # 可选
```

诊断：

```powershell
go run ./cmd/tb-inventory doctor --project-url "https://www.teambition.com/project/<projectId>/works/<folderId>"
```

抓取清单：

```powershell
go run ./cmd/tb-inventory inventory `
  --project-url "https://www.teambition.com/project/<projectId>/works/<folderId>" `
  --output ./output/teambition `
  --force-refresh
```

也可以使用 `--project-id`、`--projects-file` 或 `--discover-projects`。详细说明见 [docs/TEAMBITION_INVENTORY.md](docs/TEAMBITION_INVENTORY.md)。

## Teambition 旧爬虫浏览器路径

适用于没有 OpenAPI 应用授权，但当前账号可以在网页端访问项目文件库的场景。程序会打开一个临时 Chrome 或 Edge 窗口，登录成功后读取会话 Cookie，并使用 Teambition Web 接口抓取元数据。

诊断：

```powershell
go run ./cmd/tb-web-inventory doctor `
  --project-url "https://www.teambition.com/project/<projectId>/works/<folderId>"
```

抓取清单：

```powershell
go run ./cmd/tb-web-inventory inventory `
  --project-url "https://www.teambition.com/project/<projectId>/works/<folderId>" `
  --output ./output/teambition-web `
  --force-refresh
```

如果某些子文件夹返回 `403`，浏览器路径会记录到 `tb_errors.csv` 并继续抓取其他可访问目录。详细说明见 [docs/TEAMBITION_WEB_INVENTORY.md](docs/TEAMBITION_WEB_INVENTORY.md)。

## 输出文件

Teambition 清单默认输出到 `output/teambition` 或 `output/teambition-web`，主要文件包括：

- `tb_inventory.sqlite`：可恢复的 SQLite 清单数据库。
- `tb_projects.jsonl`：项目记录。
- `tb_folders.jsonl`：文件夹记录。
- `tb_works.jsonl`：文件元数据记录。
- `tb_works.csv`：便于表格查看的文件清单。
- `import_resources.jsonl`：下游导入队列格式。
- `tb_summary.json`、`tb_summary.md`：容量和数量汇总。
- `tb_errors.csv`：抓取失败记录。

本地输出目录、日志、缓存和 `.env` 不应提交到仓库。

## 开发与测试

```bash
go test ./...
go build .
go build ./cmd/tb-inventory
go build ./cmd/tb-web-inventory
```

`.env.example` 提供了官方 SDK 路径所需的环境变量模板。

## 注意事项

- 官方 SDK 路径依赖 Teambition OpenAPI 权限，`401`、`403`、`404` 会分别记录为认证、权限或资源不可见问题。
- 浏览器路径依赖未公开的 Web 接口，Teambition 页面接口变化时可能需要更新解析逻辑。
- 当前版本统计的是 API 返回的当前文件大小，不包含历史版本容量。
- 服务器容量评估需要额外考虑副本、备份、数据库、日志和增长余量，不能只按原始文件大小估算。
