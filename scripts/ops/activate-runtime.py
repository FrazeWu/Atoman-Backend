#!/usr/bin/env python3
"""Install immutable binaries, non-root systemd overrides, and rollback on failure.
Does not migrate databases. The caller must verify backups and schema first.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import subprocess as sp
import tempfile
import time
import urllib.request


def run(args):
    return sp.run(args, check=True)


def atomic_text(path, text, mode=0o644):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as out:
            out.write(text)
        os.chmod(temp, mode)
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def unit_override(kind, service, release):
    user = 'atoman' if kind == 'backend' else 'atoman-mirror'
    binary = {'atoman-backend':'start_server','atoman-music-import-worker':'music_import_worker', 'atoman-mirror':'hubproxy'}[service]
    arguments = ' --mode prod' if service == 'atoman-backend' else ''
    text = f'''[Service]
User={user}
Group={user}
WorkingDirectory=/var/lib/{user}
ExecStart=
ExecStart={release}/{binary}{arguments}
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictNamespaces=true
LockPersonality=true
CapabilityBoundingSet=
AmbientCapabilities=
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK
StateDirectory={user}
StateDirectoryMode=0750
LogsDirectory={user}
LogsDirectoryMode=0750
TimeoutStopSec=90
'''
    if kind == 'backend':
        text += '''EnvironmentFile=
EnvironmentFile=/etc/atoman/backend.env
Environment="LOG_DIR=/var/log/atoman" "HTTP_HOST=127.0.0.1" "RUN_MIGRATIONS_ON_START=false" "TMPDIR=/var/lib/atoman/tmp"
'''
    else:
        text += '''LoadCredential=mirror-config:/etc/hubproxy/config.toml
Environment="CONFIG_PATH=%d/mirror-config" "SERVER_HOST=127.0.0.1" "SERVER_PORT=50001"
'''
    return text


def healthy(url):
    for _ in range(45):
        try:
            with urllib.request.urlopen(url, timeout=3) as result:
                if result.status == 200:
                    return
        except Exception:
            pass
        time.sleep(1)
    raise RuntimeError('Health check failed: ' + url)


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--kind',choices=['backend','mirror'],required=True)
    p.add_argument('--binary-directory',type=Path,required=True)
    p.add_argument('--revision',required=True)
    p.add_argument('--environment-file',type=Path)
    p.add_argument('--dry-run',action='store_true')
    a=p.parse_args()
    if not re.fullmatch('[a-f0-9]{40}',a.revision):
        p.error('A full Git commit ID is required')
    user='atoman' if a.kind=='backend' else 'atoman-mirror'
    services=['atoman-backend','atoman-music-import-worker'] if a.kind=='backend' else ['atoman-mirror']
    names=['start_server','music_import_worker'] if a.kind=='backend' else ['hubproxy']
    hashes={name:hashlib.sha256((a.binary_directory/name).read_bytes()).hexdigest() for name in names}
    release=Path('/opt/atoman')/a.kind/'releases'/(a.revision[:12]+'-'+hashes[names[0]][:12])
    if a.dry_run:
        print(json.dumps({s:unit_override(a.kind,s,release) for s in services},indent=2));return
    if os.geteuid()!=0:
        p.error('Must run as root')
    os.umask(0o077)
    try:
        account=pwd.getpwnam(user)
    except KeyError:
        run(['useradd','--system','--home-dir','/var/lib/'+user,'--shell','/usr/sbin/nologin','--user-group',user])
        account=pwd.getpwnam(user)
    for directory in [Path('/var/lib')/user,Path('/var/log')/user,Path('/var/lib')/user/'tmp']:
        directory.mkdir(parents=True,exist_ok=True,mode=0o750)
        os.chown(directory,account.pw_uid,account.pw_gid);os.chmod(directory,0o750)
    if a.kind == 'backend' and a.environment_file:
        source = a.environment_file.parent / 'uploads'
        destination = Path('/var/lib/atoman/uploads')
        if source.is_dir() and not destination.exists():
            shutil.copytree(source, destination, symlinks=True)
            for item in [destination, *destination.rglob('*')]:
                os.chown(item, account.pw_uid, account.pw_gid, follow_symlinks=False)
    release.mkdir(parents=True,exist_ok=True,mode=0o755)
    for parent in [Path('/opt/atoman'),Path('/opt/atoman')/a.kind,release.parent,release]:
        os.chmod(parent,0o755)
    for name in names:
        target=release/name
        if target.exists() and hashlib.sha256(target.read_bytes()).hexdigest()!=hashes[name]:
            raise RuntimeError('Immutable release collision')
        if not target.exists():
            shutil.copy2(a.binary_directory/name,target);os.chmod(target,0o755)
    atomic_text(release/'manifest.json',json.dumps({'revision':a.revision,'sha256':hashes},indent=2))
    rollback=Path('/var/backups/atoman')/('runtime-'+a.kind+'-'+dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ'))
    rollback.mkdir(parents=True,mode=0o700)
    old={}
    env_path=Path('/etc/atoman/backend.env')
    env_old=env_path.read_bytes() if env_path.exists() else None
    if a.kind=='backend':
        if not a.environment_file or not a.environment_file.is_file():
            p.error('Backend environment file is required')
        atomic_text(env_path,a.environment_file.read_text(),0o600)
    for service in services:
        drop=Path('/etc/systemd/system')/(service+'.service.d')/'90-managed-runtime.conf'
        old[str(drop)]=drop.read_text() if drop.exists() else None
        (rollback/(service+'.previous.txt')).write_text(sp.check_output(['systemctl','cat',service],text=True))
        atomic_text(drop,unit_override(a.kind,service,release))
    (rollback/'overrides.json').write_text(json.dumps(old,indent=2))
    try:
        run(['systemctl','daemon-reload'])
        for service in services:
            run(['systemctl','restart',service])
        healthy('http://127.0.0.1:8080/readyz' if a.kind=='backend' else 'http://127.0.0.1:50001/ready')
        for service in services:
            run(['systemctl','is-active','--quiet',service])
            pid=int(sp.check_output(['systemctl','show',service,'-p','MainPID','--value'],text=True))
            if os.stat('/proc/'+str(pid)).st_uid!=account.pw_uid:
                raise RuntimeError('Service is not running under dedicated UID')
        link=release.parent.parent/'current'
        temp=link.with_name('current.new')
        if temp.exists() or temp.is_symlink():temp.unlink()
        temp.symlink_to(release);os.replace(temp,link)
        print(json.dumps({'kind':a.kind,'revision':a.revision,'release':str(release),'user':user,'rollback':str(rollback),'status':'healthy'}))
    except Exception:
        for name,content in old.items():
            if content is None:Path(name).unlink(missing_ok=True)
            else:atomic_text(Path(name),content)
        if a.kind=='backend':
            if env_old is None:env_path.unlink(missing_ok=True)
            else:atomic_text(env_path,env_old.decode(),0o600)
        run(['systemctl','daemon-reload'])
        for service in services:sp.run(['systemctl','restart',service])
        raise

if __name__=='__main__':main()
