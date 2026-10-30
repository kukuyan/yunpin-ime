"""Exercise the actual Git tar export and extraction on the host platform."""
import io
import subprocess
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / 'scripts' / 'extract-git-archive.py'


class GitArchiveExtraction(unittest.TestCase):
    def test_git_archive_unicode_and_subtree(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            repo = root / 'repo'
            repo.mkdir()
            subprocess.run(['git', 'init', '-q', str(repo)], check=True)
            source = repo / 'nested' / '扩展-Unicode_compressed.webp'
            source.parent.mkdir()
            source.write_bytes(b'fixture\x00\xff')
            subprocess.run(['git', '-C', str(repo), 'add', '.'], check=True)
            subprocess.run(['git', '-C', str(repo), '-c', 'user.name=Fixture', '-c',
                            'user.email=fixture@example.invalid', 'commit', '-qm', 'fixture'], check=True)
            for strip in (0, 1):
                archive = root / f'{strip}.tar'
                args = ['git', '-C', str(repo), 'archive', '--format=tar', f'--output={archive}', 'HEAD']
                if strip:
                    args += ['--', 'nested']
                subprocess.run(args, check=True)
                destination = root / f'out-{strip}'
                destination.mkdir()
                subprocess.run([sys.executable, str(SCRIPT), str(archive), str(destination), str(strip)], check=True)
                output = destination / ('nested' if not strip else '') / source.name
                self.assertEqual(output.read_bytes(), b'fixture\x00\xff')

    def test_reject_unsafe_entries(self):
        for name, kind in [('../escape', tarfile.REGTYPE), ('safe/link', tarfile.SYMTYPE), ('C:/absolute', tarfile.REGTYPE)]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                archive = root / 'input.tar'
                with tarfile.open(archive, 'w') as tf:
                    info = tarfile.TarInfo(name)
                    info.type = kind
                    if kind == tarfile.SYMTYPE:
                        info.linkname = '../escape'
                        tf.addfile(info)
                    else:
                        info.size = 1
                        tf.addfile(info, io.BytesIO(b'x'))
                dest = root / 'out'
                dest.mkdir()
                result = subprocess.run([sys.executable, str(SCRIPT), str(archive), str(dest), '0'], capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / 'escape').exists())

if __name__ == '__main__':
    unittest.main()
