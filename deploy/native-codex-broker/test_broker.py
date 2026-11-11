import json
import os
from pathlib import Path
import struct
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).parent))
import forced_lease
import native_rpc
import socket_bridge


class Request:
    def __init__(self,body,uid=1000):self.body=body;self.uid=uid;self.output=b''
    def settimeout(self,v):pass
    def getsockopt(self,*args):return struct.pack('3i',123,self.uid,0)
    def recv(self,n):v=self.body;self.body=b'';return v
    def sendall(self,v):self.output+=v


class Tests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.binding={'account_id':'account-test','user_id':'user-test','host':'100.64.0.1'}
        (self.root/'binding.json').write_text(json.dumps(self.binding))
        self.lease={'source':'codex-app-server','access_token':'synthetic-access','account_id':'account-test','user_id':'user-test','expires_at':9999999999}

    def handle(self,request,result=None):
        value=types.SimpleNamespace(returncode=0,stdout=json.dumps(result or self.lease).encode())
        with patch.object(socket_bridge,'ROOT',self.root),patch.object(socket_bridge.subprocess,'run',return_value=value) as invoke:
            socket_bridge.Handler(request,None,None)
        return json.loads(request.output),invoke

    def test_lease_and_fixed_ssh_args(self):
        output,invoke=self.handle(Request(b'{"account_id":"account-test"}\n'))
        self.assertEqual(output,self.lease)
        args=invoke.call_args.args[0]
        self.assertEqual(args[-2:],['codex005@100.64.0.1','lease'])
        self.assertIn('StrictHostKeyChecking=yes',args)
        self.assertNotIn('shell',invoke.call_args.kwargs)

    def test_deny_other_local_uid(self):
        output,invoke=self.handle(Request(b'',uid=1001))
        self.assertEqual(output,{'error':'lease_unavailable'});invoke.assert_not_called()

    def test_deny_wrong_binding_without_ssh(self):
        output,invoke=self.handle(Request(b'{"account_id":"other"}\n'))
        self.assertEqual(output,{'error':'lease_unavailable'});invoke.assert_not_called()

    def test_deny_refresh_token_disclosure(self):
        lease=dict(self.lease,refresh_token='synthetic-refresh')
        output,_=self.handle(Request(b'{"account_id":"account-test"}\n'),lease)
        self.assertEqual(output,{'error':'lease_unavailable'})

    def test_deny_remote_identity_change(self):
        lease=dict(self.lease,user_id='other-user')
        output,_=self.handle(Request(b'{"account_id":"account-test"}\n'),lease)
        self.assertEqual(output,{'error':'lease_unavailable'})

    def test_forced_command_validates_binding_before_native(self):
        with patch.object(forced_lease,'CONFIG',self.root/'binding.json'),patch.object(forced_lease.sys,'stdin',types.SimpleNamespace(buffer=__import__('io').BytesIO(b'{"account_id":"other"}\n'))),patch.object(forced_lease,'native_access') as native:
            with self.assertRaises(ValueError):forced_lease.main()
            native.assert_not_called()


if __name__=='__main__':unittest.main()
