#!/usr/bin/env python3
"""Root-owned sgp001 timer: official native model/list -> account256 mapping."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import time

ROOT=Path('/etc/sub2api-native-codex')

def main():
    binding=json.loads((ROOT/'binding.json').read_text())
    args=['/usr/bin/ssh','-F','/dev/null','-T','-i',str(ROOT/'id_ed25519'),'-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes','-o','UserKnownHostsFile='+str(ROOT/'known_hosts'),'-o','ConnectTimeout=5','-o','ServerAliveInterval=5','-o','ServerAliveCountMax=2','codex005@'+binding['host'],'catalog']
    result=subprocess.run(args,input=json.dumps({'account_id':binding['account_id']}).encode()+b'\n',capture_output=True,timeout=180)
    if result.returncode or len(result.stdout)>8*1024*1024:raise ValueError('native catalogue unavailable')
    manifest=json.loads(result.stdout)
    if manifest.get('source')!='codex-app-server' or any(manifest[k]!=binding[k]for k in ('account_id','user_id'))or manifest['expires_at']<=time.time()+300:raise ValueError('native catalogue binding invalid')
    ids=manifest['model_ids']
    if not ids or len(ids)>100 or any(not x.startswith('gpt-')or '*'in x for x in ids):raise ValueError('native model IDs invalid')
    spec=importlib.util.spec_from_file_location('ops_library','/var/lib/sub2apipro-vnc-oauth-20261020/library.py');library=importlib.util.module_from_spec(spec);spec.loader.exec_module(library)
    api=library.API('sub2apipro',18089)
    try:
        accounts=[x for x in api.accounts()if x['id']==256]
        if len(accounts)!=1 or accounts[0]['name']!='codex005-native-http' or accounts[0]['platform']!='openai':raise ValueError('native account missing')
        exported=api.export([256])['accounts'][0];credentials=exported['credentials']
        if any(credentials.get(k)for k in ('access_token','refresh_token','id_token'))or any(credentials.get(k)!=binding[v]for k,v in (('chatgpt_account_id','account_id'),('chatgpt_user_id','user_id'))):raise ValueError('native account binding changed')
        mapping={x:x for x in ids};changed=credentials.get('model_mapping')!=mapping
        if changed:
            credentials=dict(credentials);credentials['model_mapping']=mapping
            api.request('/admin/accounts/256',{'credentials':credentials},method='PUT')
        confirmed=api.export([256])['accounts'][0]
        if confirmed['credentials']['model_mapping']!=mapping:raise ValueError('model mapping verification failed')
        public={'source':'codex005 official model/list','model_ids':ids,'version':manifest['version'],'updated_at':manifest['updated_at'],'expires_at':manifest['expires_at'],'mapping_changed':changed}
        path=Path('/var/lib/sub2api-native-egress-20261111/catalogue-sync.json');path.write_text(json.dumps(public));path.chmod(0o600)
        print(json.dumps(public))
    finally:
        if hasattr(api,'logout'):api.logout()

if __name__=='__main__':
    os.umask(0o077)
    main()
