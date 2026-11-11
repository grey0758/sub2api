import base64
import json
import sys
from pathlib import Path
import time
import unittest
sys.path.insert(0,str(Path(__file__).parent))
import native_egress

class Tests(unittest.TestCase):
    def setUp(self):
        self.binding={'account_id':'synthetic-account','user_id':'synthetic-user'}
        claims={'exp':int(time.time())+3600,'https://api.openai.com/auth':{'chatgpt_account_id':'synthetic-account','chatgpt_user_id':'synthetic-user'}}
        token='e30.'+base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip('=')+'.synthetic'
        self.headers={'authorization':'Bearer '+token,'chatgpt-account-id':'synthetic-account','user-agent':'codex_cli_rs/0.163.0 (synthetic)'}
        self.catalogue={'model_ids':['gpt-6.1-sol'],'user_agent':self.headers['user-agent'],'expires_at':time.time()+3600}
        self.body=b'{"model":"gpt-6.1-sol","store":false}'
    def test_valid_bound_request(self):native_egress.validate_request('POST','/backend-api/codex/responses',self.headers,self.body,self.binding,self.catalogue)
    def test_compact_allows_absent_store_but_rejects_persistence(self):
        native_egress.validate_request('POST','/backend-api/codex/responses/compact',self.headers,b'{"model":"gpt-6.1-sol"}',self.binding,self.catalogue)
        with self.assertRaises(ValueError):native_egress.validate_request('POST','/backend-api/codex/responses/compact',self.headers,b'{"model":"gpt-6.1-sol","store":true}',self.binding,self.catalogue)
    def test_no_arbitrary_destination_or_method(self):
        for method,path in [('POST','https://evil.test/backend-api/codex/responses'),('POST','/v1/responses'),('GET','/backend-api/codex/responses'),('POST','/backend-api/codex/responses?x=1')]:
            with self.assertRaises(ValueError):native_egress.validate_request(method,path,self.headers,self.body,self.binding,self.catalogue)
    def test_unsupported_model_rejected_before_dispatch(self):
        with self.assertRaises(ValueError):native_egress.validate_request('POST','/backend-api/codex/responses',self.headers,b'{"model":"gpt-invented","store":false}',self.binding,self.catalogue)
    def test_other_identity_rejected(self):
        for key,value in [('chatgpt-account-id','other'),('user-agent','codex_cli_rs/0.999.0')]:
            headers=dict(self.headers);headers[key]=value
            with self.assertRaises(ValueError):native_egress.validate_request('POST','/backend-api/codex/responses',headers,self.body,self.binding,self.catalogue)
    def test_expired_native_catalogue_rejected(self):
        self.catalogue['expires_at']=time.time()-1
        with self.assertRaises(ValueError):native_egress.validate_request('POST','/backend-api/codex/responses',self.headers,self.body,self.binding,self.catalogue)
    def test_final_http_policy_and_order_match_native_proxy(self):
        headers=dict(self.headers,**{'version':'0.999.0','x-codex-routing-hint':'model=bad','accept-encoding':'gzip','session-id':'s','thread-id':'s','accept':'text/event-stream','content-type':'application/json'})
        result=native_egress.ordered_headers(headers,self.headers['user-agent']);self.assertNotIn('version',result);self.assertNotIn('x-codex-routing-hint',result);self.assertEqual(result['Accept-Encoding'],'identity');self.assertEqual(result['Connection'],'close');self.assertEqual(result['Host'],'chatgpt.com');self.assertLess(list(result).index('session-id'),list(result).index('authorization'))

if __name__=='__main__':unittest.main()
