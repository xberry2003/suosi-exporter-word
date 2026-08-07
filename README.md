# 所思导出与 Teambition 项目清单工具

这个仓库包含三部分能力：

- `go run .`：原有所思知识库导出工具，将 `thoughts.teambition.com` Workspace 导出为 `docx` 或 `html`。
- `go run ./cmd/tb-inventory`：Teambition 官方 OpenAPI SDK 路径，用 App ID / App Secret 抓取项目文件元数据。
- `go run ./cmd/tb-web-inventory`：旧爬虫浏览器会话路径，打开临时浏览器登录后，使用 Web 接口抓取项目文件元数据。
- `go run ./cmd/tb-files`：项目文件库采集器，发现来源目录树和元数据，并可选下载文件本体。

所有 Teambition 命令均只采集来源数据；它们不会调用 TeamFlow、写入 TeamFlow 数据库或生成 TeamFlow 标识。

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

## Teambition 浏览器批量清单

适用于没有 OpenAPI 应用授权，但当前账号可以在网页端访问项目文件库的场景。该命令只有一个批量入口，不扫描企业首页；它直接读取已经准备好的 `tb_discovered_projects.json`，在一个持续打开的 Chrome 或 Edge 会话中依次访问所有 `/works/...` URL。

```powershell
go run ./cmd/tb-web-inventory `
  --projects-json "D:\桌面\suosi-export\tb_discovered_projects.json"
```

浏览器登录一次后会在整个批次中保持打开。登录资料保存在输出目录的 `browser-profile`，中断后再次运行通常不需要重新登录。已完成项目会通过 SQLite 检查点自动跳过，首轮失败项目会在最后统一重试一次。

每个项目完成后，完整清单会写入原 `tb_discovered_projects.json` 的 `crawl` 字段，并在该文件旁同步生成独立的 `tb_summary.json`；原始 `projects` URL 数组不会被覆盖。某些子文件夹返回 `403` 时会记录到 `tb_errors.csv` 并继续抓取其他可访问目录。详细说明见 [docs/TEAMBITION_WEB_INVENTORY.md](docs/TEAMBITION_WEB_INVENTORY.md)。

## Teambition 项目文件库采集与下载

`tb-files` 是面向下游标准文件采集包的两阶段命令。`discover` 完整分页采集目录树和文件元数据；`download` 可选下载文件本体，并流式校验大小和 SHA-256。默认使用浏览器 Cookie 适配器；SDK 适配器需要已有 OpenAPI 授权。`offline` 仅用于对已有采集包补建或校验下载视图，不会访问 Teambition。

首次采集目录树（浏览器会打开，登录后会继续执行）：

```powershell
go run ./cmd/tb-files discover `
  --source browser `
  --project-url "https://www.teambition.com/project/<projectId>/works/<rootId>" `
  --output ./output/teambition-files `
  --resume `
  --include-raw
```

在确认目录语义、稳定 ID 和父子关系后，再下载二进制文件：

```powershell
go run ./cmd/tb-files download `
  --source browser `
  --project-url "https://www.teambition.com/project/<projectId>/works/<rootId>" `
  --output ./output/teambition-files `
  --resume `
  --concurrency 4 `
  --max-file-size 0
```

如下载已完成但缺少浏览目录，可离线补建，且不会重新请求来源：

```powershell
go run ./cmd/tb-files download `
  --source offline `
  --project-url "https://www.teambition.com/project/<projectId>/works/<rootId>" `
  --output ./output/teambition-files `
  --resume
```

输出位于 `<output>/teambition-file-collector/<projectId>/`：

- `entities/project_file_nodes.jsonl`：来源节点、稳定来源 ID、父子关系、元数据和本地资产引用，是目录语义的权威记录。
- `entities/project_file_versions.jsonl`、`entities/project_file_references.jsonl`：仅在来源接口有实际证据时写入；无法确认时在 manifest 中标记为 unavailable。
- `assets/sha256/<prefix>/<sha256>`：按内容哈希保存的权威二进制资产。来源文件名不参与资产路径或身份判定。
- `view/`：供人工审查的可浏览镜像，尽量保持来源目录层级和文件名；同目录同名文件会追加来源 ID 防冲突。它不作为导入身份或幂等键依据。
- `view/view_manifest.json`：来源节点 ID 到实际浏览路径的映射。
- `checkpoints/`、`download_errors.jsonl`、`manifest.json`：断点状态、单文件失败和本次采集包统计。

临时签名下载 URL、Cookie 和 Token 不会写入普通日志、实体 JSONL、fingerprint 或 manifest。`--retry-failed-downloads` 可按来源 `external_id` 重试失败下载；`--external-id` 可只重试一个来源文件。

## Codex 内置浏览器 Skill：发现项目文件库 URL

如果只需要从 Teambition“我参与的项目”入口收集项目文件库 URL，而不抓取文件内容，可以使用 [find-tbProj-skill](find-tbProj-skill/)。

这个 Skill 使用 Codex Desktop 的内置浏览器和已有登录态，识别项目页面中的“文件”或“项目文件”入口，输出 `/project/{projectId}/works/{rootParentId}` URL，并支持增量写入、断点续跑、按项目 ID 去重和失败重试。它不使用官方 SDK，也不会读取或提交 Cookie、密码等登录信息。

详细流程和 JSON 格式见 [find-tbProj-skill/SKILL.md](find-tbProj-skill/SKILL.md)。

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
