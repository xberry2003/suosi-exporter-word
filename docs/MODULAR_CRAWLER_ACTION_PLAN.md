# 所思 / TB 模块化采集页面行动方案

> 实施状态（2026-08-17）：Phase 0 已完成，并已提前落地首版 SQLite 作业状态、三模块 API 和统一控制台。当前范围固定为所思导出、TB 文件下载、TB 任务采集；URL 自动发现暂不接入。实际基线见 `docs/PHASE0_BASELINE.md`。

## 1. 现状结论

当前仓库是一个 Go 1.26 单模块，底层采集能力已经存在，但产品入口仍按命令拆分：

```text
suosi-exporter-word/
  main.go                         所思 Workspace 导出 + 极简 HTTP 页面
  libs/logic/                     所思正文、模板、HTML/DOCX、资源与 manifest
  cmd/tb-templates/               单个所思知识库模板导出
  cmd/tb-templates-all/           登录后发现多个所思知识库并批量导出模板
  cmd/tb-inventory/               TB 官方 SDK：项目/文件清单、summary/export/doctor
                                  以及 tasks probe/collect（MCP 任务采集）
  cmd/tb-web-inventory/           使用浏览器 Cookie 的 TB 项目批量清单
  cmd/tb-files/                   TB 文件库两阶段采集：discover + download/offline
  internal/tbinventory/            SQLite、项目/文件模型、分页、重试、导出
  internal/tbweb/                 浏览器会话与 Cookie/HTTP 适配
  internal/teambition/collector/  任务项目采集、断点、实体 JSONL、校验
  internal/teambition/taskprobe/  MCP 客户端
  internal/teambition/filecollector/ 文件目录树、资产、校验和、断点
  find-tbProj-skill/              Codex 内置浏览器 Skill：发现 /works 文件库 URL
  schemas/                        项目、任务、文件、附件等 JSON Schema
```

已存在的用户能力：

- 所思：Workspace 正文导出为 DOCX/HTML；可选导出模板；支持覆盖、失败重试、dry-run、mock，manifest 和失败记录。
- TB 项目/文件：官方 SDK 清单；浏览器会话批量清单；文件目录发现；可选下载文件本体、SHA-256、断点恢复和离线补建视图。
- TB 任务：MCP `tasks probe/collect`，可采集项目、阶段、用户、任务、关系和任务关联文件；注释和活动明确不在当前合同内。
- URL：`find-tbProj-skill` 可从“我参与的项目”发现合法 `/project/{id}/works/{root}` URL，并增量写 JSON。

当前缺少的产品能力：统一页面、统一认证/浏览器会话、模块选择器、批量输入、作业队列、实时进度、取消/重试、作业历史、结果浏览与下载、权限/敏感信息保护、统一错误模型。

## 2. 推荐目标形态

采用“单 Go 服务 + 本地作业引擎 + 统一页面/API”的模块化单体。保留现有 CLI 作为兼容入口，页面只调用内部服务层，不通过 shell 启动 CLI。

```text
Browser UI
   |
HTTP API (/api/modules, /api/jobs, /api/jobs/:id/events, /api/artifacts)
   |
Job Service  ---- SQLite (jobs, steps, events, credentials metadata)
   |
Module Runner interface
   |-- thoughts-exporter       -> libs/logic.ExportWorkspace
   |-- tb-url-discovery        -> Skill/浏览器适配层或导入 URL 文件
   |-- tb-file-inventory       -> tbinventory / tbweb
   |-- tb-file-collector        -> filecollector discover/download
   |-- tb-task-collector        -> collector + taskprobe
   |
Artifact Store (每个 job 独立目录，复用现有 manifest/entities/assets 输出)
```

第一版建议使用 Go 标准库 HTML/轻量 JS，避免在当前无前端工程的仓库中同时引入 React 构建链。若后续需要复杂表格、树和可视化，再把 UI 独立成 `web/` 前端；后端 API 契约保持不变。

## 3. 统一模块契约

每种采集器实现同一接口，业务逻辑继续留在现有 `libs`/`internal` 包：

```go
type Module interface {
    ID() string
    Describe() ModuleInfo
    Validate(ctx context.Context, input JobInput) error
    Run(ctx context.Context, input JobInput, report ProgressReporter) (Result, error)
}
```

`JobInput` 使用 JSON 保存，包含 `module_id`、来源 URL/项目 ID、输出策略、resume、include_raw、download_assets、concurrency、since 等模块参数。每次运行生成唯一 `job_id`，输出固定在 `output/jobs/<job_id>/`，并在结果中引用已有 manifest/summary/checksum 文件。

进度事件至少包含：`queued/running/succeeded/partial/failed/cancelled`、当前 step、已完成/总数（未知时为 null）、成功/失败计数、字节数、可重试标记、脱敏错误信息。采集器应通过 reporter 上报事件，不能由页面解析 stdout。

## 4. 页面建议

第一屏是“新建采集任务”：

1. 选择模块（所思、TB 文件、TB URL、任务面板），显示模块能力和所需凭据。
2. 输入来源：单 URL、项目 URL 列表/JSON、项目 ID；高级参数按模块展开。
3. 预检：URL 解析、凭据存在性、输出目录可写性、预计覆盖范围；预检不启动采集。
4. 启动作业后进入任务面板：总状态、阶段进度、当前项目/文件、速率、错误、暂停/取消、重试失败项。
5. 历史任务：按模块、状态、时间筛选；可打开 manifest/summary，下载导出包或从断点继续。

页面不展示 Cookie、Token、签名下载 URL；日志和错误响应统一脱敏。

## 5. 分阶段实施

### Phase 0：基线与边界（1 天）

- 固化模块清单、输入字段和输出目录规范。
- 将 `go test` 的构建缓存改到仓库可写临时目录后重新跑全量测试；记录当前基线。
- 评估 `find-tbProj-skill` 的调用边界：页面服务不能直接依赖 Codex UI Skill，应提供“导入已发现 URL JSON”作为可靠路径，浏览器发现另做适配器。

### Phase 1：作业引擎与统一 API（2~3 天）

- 新增 `internal/jobs`：Job/Step/Event 模型、SQLite schema、状态机、取消、重试、resume。
- 新增 `internal/modules`：模块注册表、输入校验、Module 接口和 5 个适配器。
- API：`GET /api/modules`、`POST /api/jobs`、`GET /api/jobs`、`GET /api/jobs/{id}`、`POST /api/jobs/{id}/cancel`、`POST /api/jobs/{id}/retry`、`GET /api/jobs/{id}/events`。
- 事件先用 Server-Sent Events；断线后按 `last_event_id` 补发，避免 WebSocket 首版运维复杂度。

### Phase 2：页面 MVP（2~3 天）

- 替换 `main.go` 的极简表单，保留旧 URL/CLI 参数兼容。
- 新建任务、任务详情、历史任务、结果下载四个视图。
- 统一展示 partial 与 failed，支持从 checkpoint 继续；不再使用全局 `running`。

### Phase 3：能力接入与安全（3~5 天）

- 先接入所思、TB 文件 discover/download、TB task collect；再接 TB URL discovery。
- 浏览器模块使用按 job 隔离的 profile；官方 SDK/MCP 凭据只从环境变量或本机安全配置读取。
- 路径限制在配置的 output root，防止用户输入路径穿越；下载增加大小、超时、并发和磁盘余量检查。

### Phase 4：验证与发布（2~3 天）

- 单元测试：状态机、输入校验、URL 解析、事件重放、取消、断点恢复。
- 集成测试：mock 所思/TB/MCP 服务，验证每个模块能产生标准 Result 和 manifest 引用。
- Playwright 测试：四模块创建任务、实时进度、刷新恢复、失败重试、结果下载；覆盖桌面和窄屏。
- 发布前检查：日志脱敏、并发上限、文件权限、旧 CLI 回归、输出 schema 校验。

## 6. 必须先解决的现有问题

- `main.go` 的任务完成后 `os.Exit(0)` 必须移除；服务进程不能因一次页面任务退出。
- `runMu/running` 只能表达单布尔状态，替换为 SQLite 作业状态和 worker semaphore。
- 所思导出当前在 `ExportWorkspace` 内部创建日志并同步执行，应增加 context 取消和 ProgressReporter 注入点。
- `tb-web-inventory`、`tb-files`、所思导出分别管理浏览器/登录态，需抽出 BrowserProfile/Session 生命周期策略，避免并发复用同一个 Chrome profile。
- `cmd` 层存在较多编排逻辑；新页面适配器应调用 `internal`/`libs`，逐步把 CLI 的参数解析与业务执行分离。
- 输出结果目录目前有仓库内大体积实验数据；产品代码、运行输出和浏览器 profile 必须继续通过 `.gitignore` 隔离。

## 7. 验收标准

- 用户可在一个页面选择四类模块并提交任务，不需要手工运行 Go 命令。
- 每个任务可查看可恢复的状态、阶段、计数、错误和输出位置；刷新页面不会丢失进度。
- 任务可取消；失败任务可重试；已完成项目/文件不会因 resume 重复下载。
- 所思输出仍符合现有 manifest；TB 文件仍保留 entities/assets/checkpoints；任务仍符合 `collector-contract.md`；不会写入 TeamFlow。
- 任一模块失败不会污染其他任务；敏感凭据、Cookie、临时签名 URL 不出现在页面、普通日志或 manifest。
- 旧命令 `go run .`、`tb-files`、`tb-inventory`、`tb-web-inventory` 继续可用。

## 8. 推荐的第一批开发顺序

先做 `internal/jobs` + API + 所思模块适配器，快速替换现有页面并验证作业模型；随后接入 TB 文件 discover/download，因为它已有清晰的两阶段结果和断点；再接任务采集；最后接 URL discovery（优先支持 JSON 导入，浏览器自动发现作为增强项）。这个顺序能最大化复用现有稳定代码，也能先验证页面、作业、恢复和安全这四个横向基础能力。
