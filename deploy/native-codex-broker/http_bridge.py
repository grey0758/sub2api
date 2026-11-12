#!/usr/bin/env python3
"""UID-restricted local HTTP socket to source-restricted SSH native egress."""
import http.client
import io
from http.server import BaseHTTPRequestHandler
import json
import os
from pathlib import Path
import socket
import select
import socketserver
import struct
import subprocess
import threading

ROOT=Path('/etc/sub2api-native-codex')
RUNTIME=Path('/run/sub2api-native-codex')
MAX_CONCURRENT_REQUESTS=10
LIMIT=threading.BoundedSemaphore(MAX_CONCURRENT_REQUESTS)


def ssh_args(binding):
    return ['/usr/bin/ssh','-F','/dev/null','-T','-i',str(ROOT/'id_ed25519'),
            '-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes',
            '-o','UserKnownHostsFile='+str(ROOT/'known_hosts'),'-o','ConnectTimeout=5',
            '-o','ServerAliveInterval=5','-o','ServerAliveCountMax=2',
            'codex005@'+binding['host'],'native-http']


class PipeSocket:
    def __init__(self,reader):self.reader=reader
    def makefile(self,*args,**kwargs):return io.BufferedReader(self.reader)


class Handler(BaseHTTPRequestHandler):
    protocol_version='HTTP/1.1'
    def log_message(self,*args):pass
    def do_GET(self):self.forward()
    def do_POST(self):self.forward()
    def forward(self):
        proc=None;started=False;acquired=False;stop=threading.Event()
        try:
            self.connection.settimeout(3600)
            _,uid,_=struct.unpack('3i',self.connection.getsockopt(socket.SOL_SOCKET,socket.SO_PEERCRED,12))
            if uid!=1000:raise ValueError('peer denied')
            length=int(self.headers.get('Content-Length','0'))
            if self.headers.get('Transfer-Encoding') or not 0<=length<=16*1024*1024:raise ValueError('body denied')
            if self.command=='POST' and self.path not in ('/backend-api/codex/responses','/backend-api/codex/responses/compact'):raise ValueError('path denied')
            if self.command=='GET' and self.path.split('?')[0]!='/backend-api/codex/models':raise ValueError('path denied')
            body=self.rfile.read(length)
            if len(body)!=length:raise ValueError('body truncated')
            binding=json.loads((ROOT/'binding.json').read_text())
            if self.headers.get('chatgpt-account-id')!=binding['account_id']:raise ValueError('identity denied')
            acquired=LIMIT.acquire(timeout=3)
            if not acquired:raise ValueError('busy')
            proc=subprocess.Popen(ssh_args(binding),stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,bufsize=0)
            proc.stdin.write(('%s %s HTTP/1.1\r\n'%(self.command,self.path)).encode('ascii'))
            for k,v in self.headers.items():proc.stdin.write((k+': '+v+'\r\n').encode('latin1'))
            proc.stdin.write(b'\r\n')
            offset=0
            while offset<len(body):offset+=proc.stdin.write(body[offset:])
            proc.stdin.close()
            def cancel_when_client_closes():
                while not stop.wait(.5):
                    try:
                        if select.select([self.connection],[],[],0)[0] and self.connection.recv(1,socket.MSG_PEEK)==b'':
                            if proc.poll()is None:proc.terminate()
                            return
                    except OSError:return
            threading.Thread(target=cancel_when_client_closes,daemon=True).start()
            response=http.client.HTTPResponse(PipeSocket(proc.stdout));response.begin()
            self.send_response(response.status)
            for k,v in response.getheaders():
                if k.lower()not in ('connection','transfer-encoding'):self.send_header(k,v)
            self.send_header('Connection','close');self.end_headers();started=True
            while True:
                chunk=response.read1(65536)
                if not chunk:break
                self.wfile.write(chunk);self.wfile.flush()
            response.close()
        except (OSError,ValueError,http.client.HTTPException):
            if not started:
                payload=b'{"error":{"type":"native_egress_unavailable","message":"Native Codex egress unavailable; no alternate source used"}}'
                try:self.send_response(502);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)));self.send_header('Connection','close');self.end_headers();self.wfile.write(payload)
                except OSError:pass
        finally:
            stop.set()
            self.close_connection=True
            if proc:
                if proc.poll()is None:proc.terminate()
                try:proc.wait(timeout=3)
                except subprocess.TimeoutExpired:proc.kill();proc.wait()
            if acquired:LIMIT.release()


class Server(socketserver.ThreadingUnixStreamServer):
    daemon_threads=True
    block_on_close=False
    request_queue_size=32


if __name__=='__main__':
    os.umask(0o077)
    path=RUNTIME/'http.sock';path.unlink(missing_ok=True)
    with Server(str(path),Handler)as server:
        os.chown(path,0,1000);os.chmod(path,0o660);server.serve_forever(poll_interval=.5)
