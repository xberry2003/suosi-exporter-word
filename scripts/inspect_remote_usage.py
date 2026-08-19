import json
from pathlib import Path

import paramiko


ROOT = Path(__file__).resolve().parents[1]


def load_env(path: Path) -> dict[str, str]:
    values: dict[str, str] = {}
    for raw in path.read_text(encoding="utf-8-sig").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip()
    return values


def run(client: paramiko.SSHClient, command: str) -> str:
    _, stdout, stderr = client.exec_command(command, timeout=30)
    output = stdout.read().decode("utf-8", errors="replace")
    error = stderr.read().decode("utf-8", errors="replace")
    status = stdout.channel.recv_exit_status()
    if status != 0:
        raise RuntimeError(f"remote command failed ({status}): {error.strip()}")
    return output


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
        sections = {
            "usage_route": "cd /www/wwwroot/kdocs-airscript-sync && sed -n '990,1045p;1125,1190p;1468,1495p' server.js",
            "database_config": "cd /www/wwwroot/kdocs-airscript-sync && grep -nE 'new Database|DB_PATH|DATABASE|\.db|\.sqlite' server.js | head -80",
            "database_files": "cd /www/wwwroot/kdocs-airscript-sync && find . -maxdepth 3 -type f | grep -E '\\.(db|sqlite)$' | grep -v node_modules",
            "usage_stats": "curl -fsS 'http://127.0.0.1:18792/api/products/usage-stats?basis=events'",
            "product": "curl -fsS 'http://127.0.0.1:18792/api/products'",
            "node_recent_events": "cd /www/wwwroot/kdocs-airscript-sync && sqlite3 -header -column data/kdocs-airscript-sync.db \"SELECT id,event_id,product_id,product_name,usage_count,source,user_name,occurred_at FROM ai_product_usage_events ORDER BY id DESC LIMIT 20;\"",
            "node_product_counts": "cd /www/wwwroot/kdocs-airscript-sync && sqlite3 -header -column data/kdocs-airscript-sync.db \"SELECT product_id,product_name,COUNT(*) AS events,SUM(usage_count) AS uses,MAX(occurred_at) AS latest FROM ai_product_usage_events WHERE product_id IN (101,102) GROUP BY product_id,product_name;\"",
            "go_recent_events": "sqlite3 -header -column /www/wwwroot/suosi-control/runtime/data/jobs.sqlite \"SELECT event_id,job_id,module_id,delivery_status,delivery_attempts,last_error,occurred_at FROM usage_events ORDER BY created_at DESC LIMIT 20;\"",
            "go_runtime_env": "pm2 env 487 | grep '^SUOSI_USAGE_' || true",
        }
        results = {name: run(client, command) for name, command in sections.items()}
        products = json.loads(results["product"])
        results["product"] = json.dumps(
            [item for item in products if item.get("id") == 102],
            ensure_ascii=False,
            indent=2,
        )
        stats = json.loads(results["usage_stats"])
        results["usage_stats"] = json.dumps(
            {
                "summary": stats.get("summary"),
                "product_102": [item for item in stats.get("products", []) if item.get("id") == 102],
            },
            ensure_ascii=False,
            indent=2,
        )
        for name, value in results.items():
            print(f"\n=== {name} ===\n{value.rstrip()}")
    finally:
        client.close()


if __name__ == "__main__":
    main()
