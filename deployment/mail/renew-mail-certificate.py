#!/usr/bin/env python3
import os,subprocess
if os.environ.get('RENEWED_LINEAGE')=='/etc/letsencrypt/live/mail.atoman.org':
    subprocess.run(['/usr/bin/python3','/usr/local/libexec/atoman-mail/mail-stalwart-admin.py','renew-certificate'],check=True)
    subprocess.run(['/usr/bin/docker','restart','atoman-stalwart'],check=True,stdout=subprocess.DEVNULL)
    subprocess.run(['/usr/sbin/nginx','-t'],check=True)
    subprocess.run(['/usr/bin/systemctl','reload','nginx'],check=True)
