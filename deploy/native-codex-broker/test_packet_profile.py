import json,os,ssl,tempfile,time,unittest
from pathlib import Path
from unittest.mock import patch
import native_egress as e

class PacketProfileTests(unittest.TestCase):
    def hello(self,prefix):
        context=e.audit_tls_context(prefix)
        client=context.wrap_bio(ssl.MemoryBIO(),ssl.MemoryBIO(),server_side=False,server_hostname='chatgpt.com')
        with self.assertRaises(ssl.SSLWantReadError):client.do_handshake()
        return json.loads(prefix.with_suffix('.clienthello.json').read_text())

    def test_actual_openssl_randomized_hellos_have_same_profile(self):
        with tempfile.TemporaryDirectory()as tmp:
            a=self.hello(Path(tmp)/'a');b=self.hello(Path(tmp)/'b')
            self.assertNotEqual(a['wire_sha256'],b['wire_sha256'])
            self.assertEqual(a['normalized_sha256'],b['normalized_sha256'])
            self.assertTrue(a['profile']['ciphers']);self.assertTrue(a['profile']['signature_algorithms'])
            self.assertEqual(a['profile']['alpn'],[])

    def test_no_observer_when_disabled(self):
        context=e.audit_tls_context(None)
        self.assertIsNone(context._msg_callback)
        self.assertEqual(context.verify_mode,ssl.CERT_REQUIRED)
        self.assertTrue(context.check_hostname)

    def test_bad_hello_rejected(self):
        for raw in (b'',b'\x02'+b'0'*100):
            with self.assertRaises(ValueError):e.client_hello_profile(raw)

    def test_private_fresh_capture_is_marker_scoped_and_bounded(self):
        with tempfile.TemporaryDirectory()as tmp:
            base=Path(tmp);state=base/'egress';audit=state/'packet-audit';audit.mkdir(parents=True,mode=0o700)
            directory=base/'sub2api-cli-align-test'/'production';directory.mkdir(parents=True,mode=0o700)
            control={'expires_at':time.time()+600,'marker':'a'*32,'directory':str(directory)}
            (audit/'control.json').write_text(json.dumps(control))
            with patch.object(e,'STATE',state),patch.object(e,'AUDIT',audit):
                self.assertIsNone(e.audit_start({},b'customer body without marker'))
                for _ in range(8):self.assertIsNotNone(e.audit_start({},b'a'*32))
                self.assertIsNone(e.audit_start({},b'a'*32))
                self.assertEqual(len(list(directory.glob('*.intent'))),8)

    def test_expiry_and_public_or_symlink_directory_do_not_capture(self):
        with tempfile.TemporaryDirectory()as tmp:
            base=Path(tmp);state=base/'egress';audit=state/'packet-audit';audit.mkdir(parents=True,mode=0o700)
            directory=base/'sub2api-cli-align-test'/'production';directory.mkdir(parents=True,mode=0o700)
            control={'expires_at':time.time()-1,'marker':'a'*32,'directory':str(directory)}
            with patch.object(e,'STATE',state),patch.object(e,'AUDIT',audit):
                (audit/'control.json').write_text(json.dumps(control));self.assertIsNone(e.audit_start({},b'a'*32))
                control['expires_at']=time.time()+600;directory.chmod(0o755)
                (audit/'control.json').write_text(json.dumps(control));self.assertIsNone(e.audit_start({},b'a'*32))
                directory.chmod(0o700);link=directory.parent/'link';link.symlink_to(directory,target_is_directory=True);control['directory']=str(link)
                (audit/'control.json').write_text(json.dumps(control));self.assertIsNone(e.audit_start({},b'a'*32))

if __name__=='__main__':unittest.main()
