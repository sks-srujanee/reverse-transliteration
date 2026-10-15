#!/usr/bin/env python3
"""Extract only the reverse executable from the supplied distribution."""
import argparse
import hashlib
import os
from pathlib import Path
import platform
import tempfile
import zipfile

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('archive', type=Path)
parser.add_argument('--target', choices=['darwin-arm64', 'darwin-amd64', 'linux-amd64'])
args = parser.parse_args()
machine = platform.machine()
architecture = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64', 'AMD64': 'amd64'}.get(machine, machine)
native = f'{platform.system().lower()}-{architecture}'
target = args.target or native
if target not in ('darwin-arm64', 'darwin-amd64', 'linux-amd64'):
    parser.error(f'Unsupported platform: {target}')
destination = Path(__file__).resolve().parent / 'bin' / ('translit-reverse-' + target if args.target else 'translit-reverse')
destination.parent.mkdir(exist_ok=True)
with zipfile.ZipFile(args.archive) as archive:
    data = archive.read(f'gotrans_reverse_dist/translit-reverse-{target}')
with tempfile.NamedTemporaryFile(dir=destination.parent, delete=False) as temporary:
    temporary.write(data)
    temporary_path = Path(temporary.name)
try:
    temporary_path.chmod(0o755)
    os.replace(temporary_path, destination)
finally:
    temporary_path.unlink(missing_ok=True)
print(f'{destination}\nSHA256 {hashlib.sha256(data).hexdigest()}')
