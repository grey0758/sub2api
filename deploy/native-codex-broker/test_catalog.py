import unittest
from native_catalog import supported_model_ids


class Tests(unittest.TestCase):
    def test_rejects_retired_client_entry_and_hidden_backend_entries(self):
        rows=[{'model':'gpt-current'},{'model':'gpt-retired'},{'model':'gpt-hidden','hidden':True}]
        backend=[{'slug':'gpt-current'},{'slug':'gpt-hidden'},{'slug':'gpt-backend-only'}]
        ids,visible,available=supported_model_ids(rows,backend)
        self.assertEqual(ids,['gpt-current'])
        self.assertIn('gpt-retired',visible)
        self.assertIn('gpt-backend-only',available)

    def test_empty_account_intersection_fails_closed(self):
        with self.assertRaises(ValueError):supported_model_ids([{'model':'gpt-retired'}],[])


if __name__=='__main__':unittest.main()
