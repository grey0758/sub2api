#!/usr/bin/env python3
"""Application-UID-only local UNIX broker; SSH key can execute only forced_lease.py.

No tokens are cached or written. The application sees only a socket, never the
SSH key or native auth files. stdout/stderr of SSH stay in process memory.
"""
import json
import os
from pathlib import Path
import socket
import socketserver
import struct
import subprocess
import threading

ROOT = Path('/etc/sub2api-native-codex')
RUNTIME = Path('/run/sub2api-native-codex')
LIMIT = threading.BoundedSemaphore(2)
CLIENT_UID = 1000
CLIENT_GID = 1000


class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.settimeout(30)
        try:
            _, uid, _ = struct.unpack('3i', self.request.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
            if uid != CLIENT_UID:
                raise ValueError('peer denied')
            data = bytearray()
            while b'\n' not in data:
                part = self.request.recv(1024)
                if not part:
                    raise ValueError('missing request')
                data.extend(part)
                if len(data) > 2048:
                    raise ValueError('request too large')
            request = json.loads(data.split(b'\n', 1)[0])
            binding = json.loads((ROOT / 'binding.json').read_text())
            if set(request) != {'account_id'} or request['account_id'] != binding['account_id']:
                raise ValueError('identity denied')
            if not LIMIT.acquire(timeout=3):
                raise ValueError('busy')
            try:
                result = subprocess.run([
                    '/usr/bin/ssh', '-F', '/dev/null', '-T', '-i', str(ROOT / 'id_ed25519'),
                    '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
                    '-o', 'UserKnownHostsFile=' + str(ROOT / 'known_hosts'), '-o', 'ConnectTimeout=5',
                    '-o', 'ServerAliveInterval=5', '-o', 'ServerAliveCountMax=2',
                    'codex005@' + binding['host'], 'lease'],
                    input=json.dumps(request).encode() + b'\n', capture_output=True, timeout=22)
            finally:
                LIMIT.release()
            if result.returncode or len(result.stdout) > 32768:
                raise ValueError('native lease failed')
            lease = json.loads(result.stdout)
            if set(lease) != {'source','access_token','account_id','user_id','expires_at'}:
                raise ValueError('invalid lease')
            if any(lease[k] != binding[k] for k in ('account_id','user_id')):
                raise ValueError('binding changed')
            self.request.sendall(json.dumps(lease).encode() + b'\n')
        except Exception:
            try:
                self.request.sendall(b'{"error":"lease_unavailable"}\n')
            except OSError:
                pass


class Server(socketserver.ThreadingUnixStreamServer):
    daemon_threads = True
    block_on_close = False


if __name__ == '__main__':
    os.umask(0o077)
    RUNTIME.mkdir(mode=0o750, parents=True, exist_ok=True)
    os.chown(RUNTIME, 0, CLIENT_GID)
    os.chmod(RUNTIME, 0o750)
    path = RUNTIME / 'token.sock'
    path.unlink(missing_ok=True)  # RuntimeDirectory belongs only to this service.
    with Server(str(path), Handler) as server:
        os.chown(path, 0, CLIENT_GID)
        os.chmod(path, 0o660)
        server.serve_forever(poll_interval=.5)
