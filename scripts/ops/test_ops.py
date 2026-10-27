import importlib.util
from pathlib import Path
import unittest
import tempfile
import json

ROOT=Path(__file__).parent

def module(name):
    spec=importlib.util.spec_from_file_location(name,ROOT/(name+'.py'))
    obj=importlib.util.module_from_spec(spec);spec.loader.exec_module(obj);return obj

class OperationsContracts(unittest.TestCase):
    def test_identifier_escaping(self):
        backup=module('backup-postgres')
        self.assertEqual(backup.quote_ident('a"b'),'"a""b"')
        self.assertIn("'x''y'",backup.count_sql(["x'y"]))
    def test_non_root_and_readonly_release(self):
        deploy=module('activate-runtime')
        for service in ['atoman-backend','atoman-music-import-worker']:
            unit=deploy.unit_override('backend',service,Path('/opt/atoman/backend/releases/test'))
            for directive in ['User=atoman\n','NoNewPrivileges=true','ProtectHome=true','ProtectSystem=strict','CapabilityBoundingSet=','RUN_MIGRATIONS_ON_START=false','HTTP_HOST=127.0.0.1']:
                self.assertIn(directive,unit)
            self.assertNotIn('/root/',unit)
    def test_retention_only_removes_own_completed_backups(self):
        backup=module('backup-postgres')
        with tempfile.TemporaryDirectory() as raw:
            root=Path(raw)
            for name in ['20260101','20260102','20260103']:
                directory=root/name; directory.mkdir()
                (directory/'COMPLETE').touch()
                (directory/'manifest.json').write_text(json.dumps({'format':'atoman-postgres-logical-v1','complete':True}))
            for name in ['unrelated','partial']:
                (root/name).mkdir()
            (root/'symlink').symlink_to(root/'20260103',target_is_directory=True)
            backup.prune_managed_backups(root,2)
            self.assertFalse((root/'20260101').exists())
            for name in ['20260102','20260103','unrelated','partial','symlink']:
                self.assertTrue((root/name).exists())

    def test_mirror_credential_and_loopback(self):
        unit=module('activate-runtime').unit_override('mirror','atoman-mirror',Path('/opt/atoman/mirror/releases/test'))
        self.assertIn('LoadCredential=mirror-config:',unit)
        self.assertIn('SERVER_HOST=127.0.0.1',unit)
        self.assertIn('User=atoman-mirror',unit)

if __name__=='__main__':unittest.main()
