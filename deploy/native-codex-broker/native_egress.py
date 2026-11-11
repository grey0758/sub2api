#!/usr/bin/env python3
"""Pinned ChatGPT HTTP egress on the official Codex owner's host; no retries."""
import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import ssl
import sys
import time
from urllib.parse import urlsplit

ROOT = Path('/etc/sub2api-native-codex')
STATE = Path('/home/codex005/.local/state/sub2api-native-egress')
AUDIT = Path('/home/codex005/.local/state/sub2api-native-egress-20261111/capture')
LIMIT = 16 * 1024 * 1024
ORDER = ['x-codex-beta-features','x-codex-window-id','x-codex-turn-metadata',
         'x-openai-internal-codex-responses-lite','x-client-request-id',
         'session-id','thread-id','accept','content-type','authorization',
         'chatgpt-account-id','originator','user-agent']
ALLOW = set(ORDER) | {'x-codex-turn-state','x-codex-installation-id','accept-language','if-none-match'}


def validate_request(method, path, headers, body, binding, catalogue):
    parsed = urlsplit(path)
    if parsed.scheme or parsed.netloc or '\\' in path:
        raise ValueError('destination denied')
    allowed = (method == 'POST' and parsed.path in ('/backend-api/codex/responses','/backend-api/codex/responses/compact')) or (method == 'GET' and parsed.path == '/backend-api/codex/models')
    if not allowed or (method == 'POST' and parsed.query):
        raise ValueError('endpoint denied')
    if headers.get('chatgpt-account-id') != binding['account_id']:
        raise ValueError('account denied')
    token = headers.get('authorization','')
    if not token.startswith('Bearer '):raise ValueError('authentication required')
    parts = token[7:].split('.')
    if len(parts) != 3:raise ValueError('token invalid')
    claims = json.loads(base64.urlsafe_b64decode(parts[1]+'==='))
    auth = claims.get('https://api.openai.com/auth',{})
    if auth.get('chatgpt_account_id') != binding['account_id'] or auth.get('chatgpt_user_id') != binding['user_id'] or claims.get('exp',0) <= time.time()+60:
        raise ValueError('token binding invalid')
    # SO_PEERCRED + restricted/source-bound SSH authenticate the sender; OpenAI
    # validates the OAuth signature. Consistency checks never mint credentials.
    if catalogue.get('expires_at',0) <= time.time():raise ValueError('native catalogue expired')
    if method == 'POST':
        value = json.loads(body)
        if value.get('model') not in catalogue['model_ids']:raise ValueError('model unavailable for native account')
        if (parsed.path.endswith('/compact') and value.get('store',False) is not False) or (not parsed.path.endswith('/compact') and value.get('store') is not False):raise ValueError('store must be false')
    if headers.get('user-agent') != catalogue['user_agent']:
        raise ValueError('native identity mismatch')


def ordered_headers(headers, user_agent):
    headers = {k.lower():v for k,v in headers.items() if k.lower() in ALLOW}
    headers['user-agent'] = user_agent
    headers['originator'] = user_agent.split('/',1)[0]
    result = {k:headers[k] for k in ORDER if k in headers}
    result.update({k:headers[k]for k in sorted(headers)if k not in result})
    # Same final HTTP policy as codex005-complete-capture/openai_proxy.py.
    result.update({'Host':'chatgpt.com','Accept-Encoding':'identity','Connection':'close'})
    return result


def audit_start(headers, body):
    try:
        control = json.loads((AUDIT/'control.json').read_text())
        if not(time.time()<control['expires_at']<=time.time()+1800) or len(control['marker'])<32 or control['marker'].encode() not in body:return None
        for index in range(16):
            prefix=AUDIT/('capture-%02d'%index)
            try:
                with (prefix.with_suffix('.intent')).open('x'):pass
                break
            except FileExistsError:continue
        else:return None
        prefix.with_suffix('.body').write_bytes(body)
        return prefix
    except (OSError,ValueError,KeyError,TypeError):return None


def run(stdin=None, stdout=None):
    stdin = stdin or sys.stdin.buffer
    stdout = stdout or sys.stdout.buffer
    line = stdin.readline(4097)
    if len(line)>4096:raise ValueError('request line too long')
    method,path,version = line.decode('ascii').strip().split(' ')
    if version!='HTTP/1.1':raise ValueError('HTTP version denied')
    headers = http.client.parse_headers(stdin)
    if headers.get('Transfer-Encoding'):raise ValueError('chunked input denied')
    length = int(headers.get('Content-Length','0'))
    if not 0<=length<=LIMIT:raise ValueError('body too large')
    body=stdin.read(length)
    if len(body)!=length:raise ValueError('truncated body')
    lower={k.lower():v for k,v in headers.items()}
    if len(lower)!=len(list(headers.items())):raise ValueError('duplicate header denied')
    binding=json.loads((ROOT/'binding.json').read_text());catalogue=json.loads((STATE/'catalogue.json').read_text())
    validate_request(method,path,lower,body,binding,catalogue)
    if method=='GET':
        rows=[]
        for source in catalogue['models']:
            if source.get('model') not in catalogue['model_ids']:continue
            row=dict(source);row['slug']=source['model'];row['display_name']=source.get('displayName',source['model'])
            row['default_reasoning_level']=source.get('defaultReasoningEffort','medium')
            row['supported_reasoning_levels']=[{'effort':x['reasoningEffort'],'description':x.get('description','')}for x in source.get('supportedReasoningEfforts',[])]
            rows.append(row)
        raw=json.dumps({'models':rows,'source':'official_codex_model_list','client_version':catalogue['version']}).encode()
        stdout.write(('HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n'%len(raw)).encode()+raw);stdout.flush();return
    outgoing=ordered_headers(lower,catalogue['user_agent'])
    prefix=audit_start(outgoing,body)
    context=ssl.create_default_context()
    actual_headers=[]
    class ObservedConnection(http.client.HTTPSConnection):
        def putheader(self,header,*values):
            actual_headers.append((header,','.join(str(v)for v in values)))
            return super().putheader(header,*values)
    conn=ObservedConnection('chatgpt.com',timeout=30,context=context)
    try:
        conn.request(method,path,body=body if length else None,headers=outgoing)
        if conn.sock:conn.sock.settimeout(3600)
        tls_info={'version':conn.sock.version(),'cipher':conn.sock.cipher(),'alpn':conn.sock.selected_alpn_protocol(),'openssl':ssl.OPENSSL_VERSION}
        local_address=conn.sock.getsockname()[0];upstream_address=conn.sock.getpeername()[0]
        response=conn.getresponse()
        if prefix:
            safe={k.lower():(hashlib.sha256(v.encode()).hexdigest()if k.lower()in ('authorization','cookie','proxy-authorization')else v)for k,v in actual_headers}
            record={'method':method,'path':path,'status':response.status,'headers':safe,'header_order':[k.lower()for k,v in actual_headers],'body_bytes':len(body),'body_sha256':hashlib.sha256(body).hexdigest(),'tls':tls_info,'local_address':local_address,'upstream_address':upstream_address,'egress_host':'us003','retry':False}
            prefix.with_suffix('.request.json').write_text(json.dumps(record))
        stdout.write(('HTTP/1.1 %d %s\r\n'%(response.status,response.reason)).encode('latin1'))
        for k,v in response.getheaders():
            if k.lower()not in ('connection','transfer-encoding','keep-alive','proxy-authenticate','proxy-authorization','upgrade'):
                stdout.write((k+': '+v+'\r\n').encode('latin1'))
        stdout.write(b'Connection: close\r\n\r\n');stdout.flush()
        while True:
            chunk=response.read1(65536)
            if not chunk:break
            stdout.write(chunk);stdout.flush()
    finally:conn.close()


if __name__=='__main__':
    os.umask(0o077)
    try:run()
    except Exception:
        # A request may already have reached OpenAI. Never retry in this layer.
        sys.exit(1)
