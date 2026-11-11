#!/usr/bin/env python3
"""Forced SSH command, run as native Codex owner. Access-token lease only."""
import fcntl
import json
import os
from pathlib import Path
import sys

from native_rpc import native_access

CONFIG = Path('/etc/sub2api-native-codex/binding.json')


def main():
    config = json.loads(CONFIG.read_text())
    request = json.loads(sys.stdin.buffer.readline(2049))
    if set(request) != {'account_id'} or request['account_id'] != config['account_id']:
        raise ValueError('identity denied')
    # Serialize acquisitions without ever implementing OAuth refresh ourselves.
    # getAuthStatus invokes the official managed-auth lifecycle. Near expiry
    # still fails closed if the native lifecycle cannot supply a valid token.
    with open(config['lock'], 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        lease = native_access(config['codex'])
    if any(lease[k] != config[k] for k in ('account_id', 'user_id')):
        raise ValueError('native identity changed')
    sys.stdout.write(json.dumps(lease) + '\n')


if __name__ == '__main__':
    os.umask(0o077)
    try:
        main()
    except Exception:
        sys.stdout.write('{"error":"lease_unavailable"}\n')
        sys.exit(1)
