# 所思知识库导出工具

一个用于导出 `thoughts.teambition.com` 知识库内容的命令行工具，也支持本地网页表单启动。

## 功能

- 登录后导出指定 Workspace 的知识库内容
- 支持按目录结构递归导出
- 支持导出为 `docx` 或 `html`
- 支持断点式跳过已成功导出的内容
- 支持 `dry-run` 预览目录树
- 支持本地 `mock` 数据调试
- 自动记录导出日志与清单文件

## 运行方式

### 1. Web 模式

直接运行程序，不传 `-url`，会自动打开本地页面：

```bash
go run .
```

打开后填写 Workspace 地址即可开始导出。

### 2. 命令行模式

```bash
go run . -url "https://thoughts.teambition.com/workspaces/xxxxxx/overview" -output exports -format docx
```

## 参数说明

- `-url` Workspace 地址
- `-output` 导出根目录，默认 `exports`
- `-format` 导出格式，支持 `docx`、`html`
- `-overwrite` 覆盖已有文件
- `-retry-failed` 重新尝试导出失败项
- `-dry-run` 仅生成预览，不下载文件
- `-mock-data` 使用本地 mock JSON 进行调试
- `-serve` 强制使用 Web 模式

## 输出内容

导出结果会按组织名和 Workspace 名称分目录保存，默认写入：

```text
exports/
logs/
```

同时会生成：

- `manifest.json`
- `failed_docs.json`
- 导出日志文件

## 开发说明

项目为 Go 程序，直接使用 `go build` 或 `go run` 即可。

```bash
go build .
```

## 注意事项

- 需要在浏览器中完成账号登录
- 导出过程依赖 `thoughts.teambition.com` 的接口可用性
- 如果 Workspace 禁止导出，程序会尝试临时开启导出权限

