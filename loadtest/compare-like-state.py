"""Inspect like load-test candidates or sample one content during a run.

Run from the Compose project directory on the test VM. No credentials are printed.
"""

from pathlib import Path
import subprocess
import sys
import time


def database_name():
    for line in Path(".env").read_text(encoding="utf-8").splitlines():
        key, _, value = line.partition("=")
        if key.strip() == "MYSQL_DATABASE":
            return value.strip().strip('"\'')
    raise RuntimeError("MYSQL_DATABASE is missing")


def query(sql):
    command = [
        "docker", "compose", "exec", "-T", "mysql", "sh", "-c",
        'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" exec mysql -uroot -N -e "$1" "$2"',
        "sh", sql, database_name(),
    ]
    return subprocess.run(command, check=True, capture_output=True, text=True).stdout.strip()


def candidates():
    sql = (
        "SELECT c.id FROM contents c WHERE c.likes_count=0 "
        "AND NOT EXISTS (SELECT 1 FROM like_states s WHERE s.content_id=c.id) "
        "ORDER BY c.id DESC LIMIT 30"
    )
    print(query(sql))


def sample(content_id, count, interval):
    print("timestamp\tcontent_id\tstates\tdesired\tactual\tcount\toutbox_pending", flush=True)
    for i in range(count):
        sql = (
            "SELECT (SELECT COUNT(*) FROM like_states WHERE content_id={id}),"
            "(SELECT COALESCE(SUM(desired_liked),0) FROM like_states WHERE content_id={id}),"
            "(SELECT COALESCE(SUM(is_liked),0) FROM like_states WHERE content_id={id}),"
            "(SELECT likes_count FROM contents WHERE id={id}),"
            "(SELECT COUNT(*) FROM outbox_msgs WHERE status='pending')"
        ).format(id=content_id)
        print(f"{time.time():.3f}\t{content_id}\t{query(sql)}", flush=True)
        if i + 1 < count:
            time.sleep(interval)


def main():
    if len(sys.argv) == 2 and sys.argv[1] == "candidates":
        candidates()
        return
    if len(sys.argv) != 5 or sys.argv[1] != "sample":
        raise SystemExit("usage: compare-like-state.py candidates | sample CONTENT_ID COUNT INTERVAL_SECONDS")
    content_id, count = int(sys.argv[2]), int(sys.argv[3])
    interval = float(sys.argv[4])
    if content_id < 1 or count < 1 or interval <= 0:
        raise SystemExit("invalid arguments")
    sample(content_id, count, interval)


if __name__ == "__main__":
    main()
