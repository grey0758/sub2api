#!/usr/bin/env python3
"""Read native model/list and identity through the pinned official app-server."""
import base64
import fcntl
import http.client
import json
import os
from pathlib import Path
import select
import ssl
import subprocess
import sys
import tempfile
import time

CONFIG=Path('/etc/sub2api-native-codex/binding.json')
STATE=Path('/home/codex005/.local/state/sub2api-native-egress')


def supported_model_ids(rows, backend_rows):
    visible={x['model']for x in rows if not x.get('hidden',False) and x.get('model','').startswith('gpt-')}
    available={x['slug']for x in backend_rows if isinstance(x.get('slug'),str)}
    ids=sorted(visible & available)
    if not ids:raise ValueError('native account has no supported visible models')
    return ids,sorted(visible),sorted(available)


def catalogue(config):
    binary=config['codex'];version=subprocess.check_output([binary,'--version'],text=True,timeout=5).strip().split()[-1]
    proc=subprocess.Popen([binary,'app-server'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,bufsize=0);pending=bytearray()
    def rpc(mid,method,params):
        payload=json.dumps({'id':mid,'method':method,'params':params}).encode()+b'\n';proc.stdin.write(payload)
        deadline=time.monotonic()+45
        while time.monotonic()<deadline:
            if b'\n'not in pending:
                if not select.select([proc.stdout],[],[],.2)[0]:continue
                chunk=os.read(proc.stdout.fileno(),1048576)
                if not chunk:raise ValueError('native exited')
                pending.extend(chunk)
                if len(pending)>8*1024*1024:raise ValueError('native output limit')
            while b'\n'in pending:
                line,_,tail=pending.partition(b'\n');pending[:]=tail;value=json.loads(line)
                if value.get('id')==mid:
                    if 'error'in value:raise ValueError('native RPC rejected')
                    return value['result']
        raise ValueError('native RPC timeout')
    try:
        initialize=rpc(1,'initialize',{'clientInfo':{'name':'us003_outer_tool_relay','version':'0.1'}});proc.stdin.write(b'{"method":"initialized"}\n')
        status=rpc(2,'getAuthStatus',{'includeToken':True,'refreshToken':False})
        if status.get('authMethod')!='chatgpt':raise ValueError('native login required')
        claims=json.loads(base64.urlsafe_b64decode(status['authToken'].split('.')[1]+'==='));auth=claims['https://api.openai.com/auth']
        if auth.get('chatgpt_account_id')!=config['account_id'] or auth.get('chatgpt_user_id')!=config['user_id']:raise ValueError('native identity mismatch')
        rows=[];cursor=None
        for mid in range(3,13):
            value=rpc(mid,'model/list',{'limit':100,'cursor':cursor,'includeHidden':False});rows.extend(value.get('data',[]));cursor=value.get('nextCursor')
            if not cursor:break
        if not rows or cursor:raise ValueError('native models incomplete')
        ua=initialize['userAgent']
        if not ua.startswith('us003_outer_tool_relay/'+version+' ')or len(ua)>512 or any(ord(c)<32 or ord(c)>126 for c in ua):raise ValueError('native user agent invalid')
        connection=http.client.HTTPSConnection('chatgpt.com',timeout=30,context=ssl.create_default_context())
        try:
            connection.request('GET','/backend-api/codex/models?client_version='+version,headers={'Authorization':'Bearer '+status['authToken'],'chatgpt-account-id':config['account_id'],'User-Agent':ua,'originator':'us003_outer_tool_relay','Accept':'application/json','Accept-Encoding':'identity','Connection':'close'})
            response=connection.getresponse();raw=response.read(1024*1024+1)
            if response.status!=200 or len(raw)>1024*1024:raise ValueError('native backend catalogue unavailable')
            backend_rows=json.loads(raw)['models']
        finally:connection.close()
        ids,visible,available=supported_model_ids(rows,backend_rows)
        if any(len(x)>128 for x in ids):raise ValueError('native models invalid')
        return {'source':'codex-app-server','capability_source':'official visible model/list intersected with native account backend manifest','account_id':config['account_id'],'user_id':config['user_id'],'version':version,'user_agent':ua,'model_ids':ids,'client_visible_model_ids':visible,'backend_model_ids':available,'models':rows,'updated_at':int(time.time()),'expires_at':int(time.time())+3600}
    finally:
        proc.stdin.close()
        try:proc.wait(timeout=3)
        except subprocess.TimeoutExpired:
            proc.terminate()
            try:proc.wait(timeout=3)
            except subprocess.TimeoutExpired:proc.kill();proc.wait()
        proc.stdout.close()


def main():
    request=json.loads(sys.stdin.buffer.readline(2049));config=json.loads(CONFIG.read_text())
    if set(request)!={'account_id'}or request['account_id']!=config['account_id']:raise ValueError('account denied')
    with open(config['lock'],'a')as lock:
        fcntl.flock(lock,fcntl.LOCK_EX);result=catalogue(config)
    STATE.mkdir(mode=0o700,exist_ok=True)
    fd,tmp=tempfile.mkstemp(prefix='.catalogue.',dir=STATE)
    with os.fdopen(fd,'w')as f:json.dump(result,f);f.flush();os.fsync(f.fileno())
    os.chmod(tmp,0o600);os.replace(tmp,STATE/'catalogue.json')
    sys.stdout.write(json.dumps(result)+'\n')


if __name__=='__main__':
    os.umask(0o077)
    main()
