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
    if set(request) not in ({'account_id'},{'account_id','include_bridge_identity'}) or request['account_id'] != config['account_id'] or ('include_bridge_identity'in request and request['include_bridge_identity'] is not True):
        raise ValueError('identity denied')
    # Serialize acquisitions without ever implementing OAuth refresh ourselves.
    # getAuthStatus invokes the official managed-auth lifecycle. Near expiry
    # still fails closed if the native lifecycle cannot supply a valid token.
    with open(config['lock'], 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        lease = native_access(config['codex'], bridge_identity=True) if request.get('include_bridge_identity') else native_access(config['codex'])
    if any(lease[k] != config[k] for k in ('account_id', 'user_id')):
        raise ValueError('native identity changed')
    sys.stdout.write(json.dumps(lease) + '\n')


if __name__ == '__main__':
    os.umask(0o077)
    try:
        command = os.environ.get('SSH_ORIGINAL_COMMAND','lease')
        if command == 'lease':main()
        elif command == 'catalog':
            from native_catalog import main as read_catalogue
            read_catalogue()
        elif command == 'native-http':
            from native_egress import run
            run()
        else:raise ValueError('command denied')
    except Exception:
        sys.stdout.write('{"error":"lease_unavailable"}\n')
        sys.exit(1)
