# Phase 0 基线与边界

## 本阶段结论

首版页面只接入三个模块：

1. 所思导出：单知识库正文，支持 DOCX/HTML，可选模板、资源、链接和校验报告。
2. TB 文件下载：项目文件库目录发现、分页、断点、文件下载、SHA-256 和浏览目录镜像。
3. TB 任务采集：项目、阶段、用户、任务、任务关系与关联文件。

TB 项目 URL 自动发现、统一用户登录、TeamFlow 写入、任务评论和活动轨迹不在首版范围内。

## 运行结构

```text
suosi-exporter-word/
  internal/control/            页面服务、作业管理、SQLite、模块适配器
    web/                       嵌入 Go 二进制的 HTML/CSS/JS
  runtime/                     本机运行数据，整体忽略
    data/jobs.sqlite           作业状态与历史
    data/browser-profiles/     浏览器模块专用登录资料
    artifacts/jobs/<job-id>/   每个任务独立产物和日志
```

页面可以为新任务指定服务端本地绝对路径。后端预检目录是否可创建、可写，并始终在所选目录下生成独立 `job-id` 子目录，避免相互覆盖。留空时继续使用 `runtime/artifacts/jobs/<job-id>`。

## API 基线

- `GET /api/health`：服务健康状态。
- `GET /api/modules`：三个可选模块及能力说明。
- `POST /api/preflight`：校验 URL、输出目录、浏览器或环境凭据。
- `POST /api/jobs`：预检通过后创建后台任务。
- `GET /api/jobs`：读取最近任务历史。
- `GET /api/jobs/{id}`：读取任务详情。
- `POST /api/jobs/{id}/cancel`：取消排队或运行中的任务。

状态模型：`queued -> running -> succeeded|partial|failed|cancelled`。服务启动时发现遗留的 `queued/running/cancelling` 任务会标记为中断失败，不会伪装成成功。

## 凭据边界

- 所思和 TB 浏览器模式：使用独立浏览器 Profile，首次运行在可见浏览器完成登录。
- 所思登录阶段使用持久 Profile、随机调试端口和 5 分钟超时，并响应任务取消；Chrome 网络错误会直接写入任务失败原因。
- TB SDK：只读取 `TB_APP_ID`、`TB_APP_SECRET`、`TB_ORG_ID` 等服务端环境变量。
- TB 任务：只读取 `TEAMBITION_MCP_HOST` 和 `TEAMBITION_MCP_TOKEN`。
- Token、Cookie 和临时签名 URL 不进入页面输入、普通日志、作业数据库或 manifest。

## 部署边界

- 当前没有应用登录系统，不允许直接暴露公网。
- 本机使用默认 `127.0.0.1:43821`；服务器使用内网监听或带认证、HTTPS 的反向代理。
- 无图形界面的服务器使用 `-open-browser=false`，避免启动时调用系统浏览器。
- `runtime/data` 与 `runtime/artifacts` 应挂载到持久磁盘并纳入备份策略。
- 浏览器模式需要图形环境；纯服务器环境优先使用 TB OpenAPI SDK。
- 首版 worker 默认并发为 1，避免多个任务争用同一个持久浏览器 Profile。

## 基线验证

- `go test ./...` 使用仓库内 `.gocache` 可通过。
- 页面和静态资源嵌入 Go 二进制，无 Node/npm 运行依赖。
- 旧 CLI 入口继续保留；页面模块直接调用 Go 内部包，不通过 shell 启动 CLI。
- `runtime/`、`logs/`、构建产物、临时下载和覆盖率文件均已加入 `.gitignore`。
