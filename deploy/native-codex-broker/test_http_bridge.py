import io
import json
from pathlib import Path
import socket
import struct
import sys
import tempfile
import threading
import types
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).parent))
import http_bridge

class Writer(io.BytesIO):
    def close(self):self.completed=self.getvalue()
class Proc:
    def __init__(self):self.stdin=Writer();self.stdout=io.BytesIO(b'HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 9\r\n\r\ndata: OK\n')
    def poll(self):return 0
    def wait(self,timeout=None):return 0
    def terminate(self):pass

class Tests(unittest.TestCase):
    def test_uid_restricted_http_stream_keeps_body(self):
        with tempfile.TemporaryDirectory()as tmp:
            root=Path(tmp);(root/'binding.json').write_text(json.dumps({'account_id':'test-account','host':'100.64.0.1'}))
            server=http_bridge.Server(str(root/'http.sock'),http_bridge.Handler);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start();proc=Proc()
            try:
                with patch.object(http_bridge,'ROOT',root),patch.object(http_bridge.os,'getuid',return_value=1000),patch.object(http_bridge.struct,'unpack',return_value=(1,1000,1000)),patch.object(http_bridge.subprocess,'Popen',return_value=proc):
                    client=socket.socket(socket.AF_UNIX);client.connect(str(root/'http.sock'));client.sendall(b'POST /backend-api/codex/responses HTTP/1.1\r\nHost: chatgpt.com\r\nchatgpt-account-id: test-account\r\nContent-Length: 2\r\n\r\n{}');response=bytearray()
                    while True:
                        part=client.recv(65536)
                        if not part:break
                        response.extend(part)
                    client.close();self.assertIn(b'200 OK',response);self.assertTrue(response.endswith(b'data: OK\n'));self.assertTrue(proc.stdin.completed.endswith(b'\r\n\r\n{}'))
            finally:server.shutdown();server.server_close()
if __name__=='__main__':unittest.main()
