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
    _, stdout, stderr = client.exec_command(command, timeout=45)
    output = stdout.read().decode("utf-8", errors="replace")
    error = stderr.read().decode("utf-8", errors="replace")
    status = stdout.channel.recv_exit_status()
    if status != 0:
        raise RuntimeError(f"remote command failed ({status}): {error.strip()}\n{output.strip()}")
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
        command = r'''set -Eeuo pipefail
CFG=/www/wwwroot/suosi-control/.teambition.env
DB=/www/wwwroot/kdocs-airscript-sync/data/kdocs-airscript-sync.db
STAMP=$(date +%Y%m%d-%H%M%S)
CFG_BAK="${CFG}.bak-product102-${STAMP}"
DB_BAK="${DB}.bak-product102-${STAMP}"
cp -a "$CFG" "$CFG_BAK"
cp -a "$DB" "$DB_BAK"
sed -i 's/^SUOSI_USAGE_PRODUCT_ID=.*/SUOSI_USAGE_PRODUCT_ID=102/' "$CFG"
set -a
. "$CFG"
set +a
pm2 restart 487 --update-env >/dev/null
sleep 3
RUNTIME_ID=$(pm2 env 487 | awk -F': ' '/^SUOSI_USAGE_PRODUCT_ID:/ {print $2}')
if [ "$RUNTIME_ID" != "102" ]; then
  cp -f "$CFG_BAK" "$CFG"
  set -a
  . "$CFG"
  set +a
  pm2 restart 487 --update-env >/dev/null
  echo "runtime product id verification failed: $RUNTIME_ID" >&2
  exit 1
fi
pm2 save >/dev/null
SQL_RESULT=$(sqlite3 "$DB" <<'SQL'
BEGIN IMMEDIATE;
UPDATE ai_product_usage_events
SET product_id = 102,
    product_name = (SELECT name FROM ai_products WHERE id = 102)
WHERE product_id = 101
  AND source = 'suosi-control'
  AND event_id LIKE 'suosi-control:%';
SELECT changes();
COMMIT;
SQL
)
HEALTH=$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:9869/api/health || true)
case "$HEALTH" in 200|401) ;; *)
  cp -f "$DB_BAK" "$DB"
  cp -f "$CFG_BAK" "$CFG"
  set -a
  . "$CFG"
  set +a
  pm2 restart 487 --update-env >/dev/null
  echo "health verification failed: $HEALTH" >&2
  exit 1
esac
echo "migrated_events=$SQL_RESULT"
echo "runtime_product_id=$RUNTIME_ID"
echo "health_http=$HEALTH"
echo "config_backup=$CFG_BAK"
echo "database_backup=$DB_BAK"
curl -fsS 'http://127.0.0.1:18792/api/products/usage-stats?basis=events'
'''
        output = run(client, command)
        lines = output.splitlines()
        stats = json.loads(lines[-1])
        product = next((item for item in stats.get("products", []) if item.get("id") == 102), None)
        print("\n".join(lines[:-1]))
        print("product_102=" + json.dumps(product, ensure_ascii=False))
    finally:
        client.close()


if __name__ == "__main__":
    main()
