import json
from pathlib import Path

import paramiko


ROOT = Path(__file__).resolve().parents[1]
TARGET_GROUP = "所思TB采集工作台"

ROWS = {
    "1": (
        "梳理所思知识库导出、TB 文件下载、TB 任务采集三类核心使用场景，明确统一控制台、服务器长期运行、登录态复用和成果下载需求。",
        "形成三模块统一工作台的需求边界，确定以成功完成一次采集任务作为一次有效使用。",
    ),
    "2": (
        "汇总现有 Go 导出代码、TB OpenAPI/MCP 能力、浏览器登录链路、员工认证系统及服务器部署环境，记录各模块输入、输出和异常场景。",
        "完成现状清单与依赖清单，确认三个模块可共用作业引擎、用户身份和服务器归档目录。",
    ),
    "3": (
        "分析模块化单体的功能边界与数据流，拆分认证、预检、作业调度、采集适配器、产物归档、ZIP 下载和使用统计职责。",
        "确定单 Go 服务、SQLite 作业引擎、模块适配器、统一页面/API 的整体架构。",
    ),
    "4": (
        "对比 Playwright、chromedp、长期 Chromium CDP、OpenAPI 与 MCP 等实现路线，评估无桌面服务器的登录态持久化方案。",
        "选定长期 Chromium 固定 Profile 配合 Python 认证恢复、Go 执行采集的混合方案，并保留人工验证入口。",
    ),
    "5": (
        "搭建统一控制台原型，接入所思导出、TB 文件下载、TB 任务采集表单及任务状态面板，验证前后端交互链路。",
        "完成可操作 Demo，三个模块均可创建任务、查看进度并下载服务器归档 ZIP。",
    ),
    "6": (
        "设计无登录营销页的工作台界面，统一模块导航、表单布局、预检反馈、任务详情、完成状态与下载入口。",
        "完成三模块响应式控制台页面，表单、状态提示和成果下载流程保持一致。",
    ),
    "7": (
        "设计 Go HTTP 服务、SQLite 作业表、并发队列、用户隔离目录、模块执行器、认证会话和统计事件存储。",
        "后端边界与数据模型落地，任务可持久化、可恢复查询，并按模块、用户和任务隔离产物。",
    ),
    "8": (
        "实现统一预检、任务状态机、取消与错误处理、登录态恢复、成功判定、幂等统计上报和失败重试逻辑。",
        "三模块共享一致的执行链路，失败与部分完成不计数，成功任务按 event_id 幂等上报。",
    ),
    "9": (
        "完成所思 DOCX/HTML 导出、TB 文件发现与下载、TB 任务及关联数据采集，并接入员工登录、服务器归档和 ZIP 传输。",
        "三个采集模块均已在服务器部署运行，可生成真实成果并由用户下载到本地。",
    ),
    "10": (
        "优化路由跳转、表单对齐、目录选择说明、预检文案、登录状态提示、任务刷新和下载操作，修复重复登录与卡住问题。",
        "主要操作路径完成收敛，用户可从登录进入对应模块并连续执行、查看和下载任务。",
    ),
    "11": (
        "补充 Go 单元测试与接口测试，验证作业存储、模块成功/失败状态、登录态复用、历史用户警告、统计幂等和产物下载。",
        "核心后端测试通过，成功任务统计、失败隔离和服务器重启后的运行行为符合预期。",
    ),
    "12": (
        "按非开发用户视角逐项试用登录、所思导出、TB 文件下载、TB 任务采集、任务查看和 ZIP 下载，记录并修复阻塞问题。",
        "完成端到端小白测试，三个模块均可从页面独立完成一次真实采集流程。",
    ),
    "13": (
        "在生产服务器完成三个模块的真实任务验收，核对登录态、采集结果、用户隔离归档、ZIP 下载及产品 102 使用统计。",
        "所思导出、TB 文件下载、TB 任务采集均验收通过；成功任务统一计入所思TB采集工作台，统计链路已验证。",
    ),
}


def load_env(path: Path) -> dict[str, str]:
    values = {}
    for raw in path.read_text(encoding="utf-8-sig").splitlines():
        line = raw.strip()
        if line and not line.startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            values[key.strip()] = value.strip()
    return values


def run(client: paramiko.SSHClient, command: str) -> str:
    _, stdout, stderr = client.exec_command(command, timeout=30)
    output = stdout.read().decode("utf-8", errors="replace")
    error = stderr.read().decode("utf-8", errors="replace")
    if stdout.channel.recv_exit_status() != 0:
        raise RuntimeError(error.strip() or output.strip())
    return output


def curl_json(client: paramiko.SSHClient, method: str, path: str, payload=None):
    command = f"curl -fsS -X {method} 'http://127.0.0.1:18792{path}'"
    if payload is not None:
        body = json.dumps(payload, ensure_ascii=False)
        command += " -H 'Content-Type: application/json' --data-raw " + json.dumps(body)
    return json.loads(run(client, command))


def main() -> None:
    env = load_env(ROOT / ".env")
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        env["SFTP_HOST"],
        port=int(env.get("SFTP_PORT", "22")),
        username=env["SFTP_USERNAME"],
        password=env["SFTP_PASSWORD"],
        timeout=15,
        allow_agent=False,
        look_for_keys=False,
    )
    try:
        groups = curl_json(client, "GET", "/api/development-groups")
        target = next((group for group in groups if group.get("name") == TARGET_GROUP), None)
        if target is None:
            raise RuntimeError(f"未找到目标开发表：{TARGET_GROUP}")
        group_id = int(target["id"])
        items = curl_json(client, "GET", f"/api/development-items?group_id={group_id}")
        indexed = {str(item.get("serial_no", "")): item for item in items}
        missing = [serial for serial in ROWS if serial not in indexed]
        if missing:
            raise RuntimeError("目标表缺少序号：" + ", ".join(missing))

        updated = []
        for serial, (content, result) in ROWS.items():
            item = indexed[serial]
            response = curl_json(
                client,
                "PUT",
                f"/api/development-items/{item['id']}",
                {"work_content": content, "work_result": result},
            )
            updated.append({
                "serial_no": response.get("serial_no"),
                "task_name": response.get("task_name"),
                "work_content": response.get("work_content"),
                "work_result": response.get("work_result"),
            })

        verified = curl_json(client, "GET", f"/api/development-items?group_id={group_id}")
        verified = [
            {
                "serial_no": row.get("serial_no"),
                "task_name": row.get("task_name"),
                "work_content": row.get("work_content"),
                "work_result": row.get("work_result"),
            }
            for row in verified
            if str(row.get("serial_no", "")) in ROWS
        ]
        verified.sort(key=lambda row: int(row["serial_no"]))
        if len(verified) != 13 or any(not row["work_content"] or not row["work_result"] for row in verified):
            raise RuntimeError("写入后的 1-13 行复查未通过")
        print(json.dumps({"group_id": group_id, "updated_count": len(updated), "rows": verified}, ensure_ascii=False, indent=2))
    finally:
        client.close()


if __name__ == "__main__":
    main()
