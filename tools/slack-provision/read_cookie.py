"""Print the encrypted Slack `d` session cookie(s) as: host_key|hex.
Reads the Chromium/Electron Cookies SQLite read-only. Slack must be fully quit
(it holds an exclusive lock while running). Decryption happens in PowerShell.

Usage: python read_cookie.py <path-to-Cookies-db>
"""
import sqlite3, sys
from pathlib import Path

db = sys.argv[1]
uri = Path(db).as_uri() + '?mode=ro&immutable=1'
con = sqlite3.connect(uri, uri=True)
try:
    rows = con.execute(
        "SELECT host_key,name,encrypted_value FROM cookies "
        "WHERE name='d' AND host_key LIKE '%slack.com%'"
    ).fetchall()
    for host, _name, ev in rows:
        print(host + '|' + bytes(ev).hex())
finally:
    con.close()
