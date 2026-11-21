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
AUDIT = STATE / 'packet-audit'
LIMIT = 16 * 1024 * 1024
ORDER = ['x-codex-beta-features','x-codex-window-id','x-codex-turn-metadata',
         'x-openai-internal-codex-responses-lite','x-client-request-id',
         'session-id','thread-id','accept','content-type','authorization',
         'chatgpt-account-id','originator','user-agent']
ALLOW = set(ORDER) | {'x-codex-turn-state','x-codex-installation-id','accept-language','if-none-match'}


def catalogue_for_client(user_agent):
    current = json.loads((STATE / 'catalogue.json').read_text())
    if current['user_agent'] == user_agent:
        return current

    # A request can already hold a lease while the broker switches binaries.
    # The release prepares the previous exact catalogue for that bounded window.
    try:
        previous = json.loads((STATE / 'catalogue.previous.json').read_text())
        remaining = previous['expires_at'] - time.time()
        if previous['user_agent'] == user_agent and 0 < remaining <= 120:
            return previous
    except (OSError, ValueError, KeyError, TypeError):
        pass
    return current


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
        directory=Path(control['directory'])
        if directory.resolve()!=directory or directory.name!='production' or not directory.parent.name.startswith('sub2api-cli-align-'):return None
        if directory.parent.parent!=STATE.parent or directory.stat().st_uid!=os.getuid() or directory.stat().st_mode&0o077:return None
        for index in range(8):
            prefix=directory/('capture-%02d'%index)
            try:
                with (prefix.with_suffix('.intent')).open('x'):pass
                break
            except FileExistsError:continue
        else:return None
        prefix.with_suffix('.body').write_bytes(body)
        return prefix
    except (OSError,ValueError,KeyError,TypeError):return None


def client_hello_profile(raw):
    """Describe actual ClientHello without random, session, or key-share bytes."""
    if len(raw)<39 or raw[0]!=1:raise ValueError('not ClientHello')
    def u16(b):return int.from_bytes(b,'big')
    def words(b):return [u16(b[i:i+2])for i in range(0,len(b),2)]
    def grease(n):return n&0x0f0f==0x0a0a and n>>8==n&255
    p=4;version=u16(raw[p:p+2]);p+=34
    p+=1+raw[p];n=u16(raw[p:p+2]);p+=2;ciphers=[x for x in words(raw[p:p+n])if not grease(x)];p+=n
    p+=1+raw[p];n=u16(raw[p:p+2]);p+=2;end=p+n
    extensions=[];groups=[];points=[];versions=[];signatures=[];alpn=[];shares=[];stable=[]
    while p<end:
        kind=u16(raw[p:p+2]);n=u16(raw[p+2:p+4]);p+=4;data=raw[p:p+n];p+=n
        if grease(kind):continue
        extensions.append(kind)
        if kind==10:groups=[x for x in words(data[2:])if not grease(x)]
        if kind==11:points=list(data[1:])
        if kind==43:versions=[x for x in words(data[1:])if not grease(x)]
        if kind==13:signatures=words(data[2:])
        if kind==16:
            q=2
            while q<len(data):m=data[q];q+=1;alpn.append(data[q:q+m].decode('ascii'));q+=m
        if kind==51:
            q=2
            while q<len(data):group=u16(data[q:q+2]);m=u16(data[q+2:q+4]);q+=4;shares.append({'group':group,'bytes':m});q+=m
        elif kind not in (21,41,44):stable.append({'type':kind,'sha256':hashlib.sha256(data).hexdigest()})
    if p!=end or end!=len(raw):raise ValueError('ClientHello length mismatch')
    profile={'legacy_version':version,'ciphers':ciphers,'extensions':extensions,'groups':groups,'point_formats':points,'supported_versions':versions,'signature_algorithms':signatures,'alpn':alpn,'key_share_shapes':shares,'stable_extensions':stable}
    ja3=','.join((str(version),'-'.join(map(str,ciphers)),'-'.join(map(str,extensions)),'-'.join(map(str,groups)),'-'.join(map(str,points))))
    return {'profile':profile,'normalized_sha256':hashlib.sha256(json.dumps(profile,sort_keys=True,separators=(',',':')).encode()).hexdigest(),'ja3':ja3,'ja3_md5':hashlib.md5(ja3.encode()).hexdigest(),'wire_bytes':len(raw),'wire_sha256':hashlib.sha256(raw).hexdigest()}


def audit_tls_context(prefix):
    context=ssl.create_default_context()
    if prefix is not None:
        def observe(connection,direction,version,content_type,message_type,data):
            if direction=='write' and int(content_type)==22 and int(message_type)==1:
                try:prefix.with_suffix('.clienthello.json').write_text(json.dumps(client_hello_profile(bytes(data))))
                except (OSError,ValueError,IndexError,UnicodeError):pass
        context._msg_callback=observe
    return context


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
    binding = json.loads((ROOT / 'binding.json').read_text())
    catalogue = catalogue_for_client(lower.get('user-agent'))
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
    context=audit_tls_context(prefix)
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
