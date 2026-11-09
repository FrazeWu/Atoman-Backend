#!/usr/bin/env python3
"""Opt-in native alias acceptance with only two new, disposable internal mailboxes.

No external delivery, no real mailbox reads, no global configuration changes.
Every created account is destroyed in a finally block; the private receipt
names only accounts that require operator cleanup and never contains secrets.
"""
import argparse
import base64
import datetime as dt
import email
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import secrets
import smtplib
import ssl
import time
import urllib.request


SOURCE = Path(__file__).with_name('mail-internal-disposable-acceptance.py')
spec = importlib.util.spec_from_file_location('mail_disposable_internal', SOURCE)
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)


def check_alias(auth, account_id, expected_name, domain_id, enabled):
    answer = base.admin_call(auth, 'x:Account/get',
                             {'ids': [account_id], 'properties': ['id', 'aliases']})
    entries = answer.get('list', [])
    if len(entries) != 1 or entries[0].get('id') != account_id:
        raise RuntimeError('Disposable account alias lookup changed')
    raw_aliases = entries[0].get('aliases', {})
    if isinstance(raw_aliases, dict):
        entries_by_slot = [(int(k), v) for k, v in raw_aliases.items() if k.isdecimal() and isinstance(v, dict)]
    elif isinstance(raw_aliases, list):
        entries_by_slot = list(enumerate(raw_aliases))
    else:
        raise RuntimeError('Unsupported live alias representation')
    matching = [i for i, item in entries_by_slot
                if item.get('name') == expected_name and item.get('domainId') == domain_id]
    if len(matching) != 1:
        raise RuntimeError('Disposable alias not uniquely present')
    index = matching[0]
    item = dict(entries_by_slot)[index]
    if item.get('enable') is not enabled:
        raise RuntimeError('Disposable alias enabled flag did not match')
    return index


def patch_alias(auth, account_id, path, value):
    answer = base.admin_call(auth, 'x:Account/set',
                             {'update': {account_id: {path: value}}})
    if answer.get('notUpdated') or set(answer.get('updated', {})) != {account_id}:
        raise RuntimeError('Disposable alias patch not acknowledged')


def identity_available(alias_address, owner_address, account_id, password):
    """Read-only native Identity/get, never create or send any identity."""
    auth = base64.b64encode((owner_address + ':' + password).encode()).decode()
    body = {'using': ['urn:ietf:params:jmap:core',
                      'urn:ietf:params:jmap:submission'],
            'methodCalls': [['Identity/get', {'accountId': account_id}, 'identity']]}
    req = urllib.request.Request(base.ADMIN_ENDPOINT, data=json.dumps(body).encode(),
                                 headers={'Authorization': 'Basic ' + auth,
                                          'Content-Type': 'application/json'}, method='POST')
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(req, timeout=12) as response:
            result = json.load(response)
        method, content, _ = result['methodResponses'][0]
        if method != 'Identity/get':
            return False
        return any(item.get('email', '').lower() == alias_address.lower()
                   for item in content.get('list', []))
    except Exception:
        return False


def send_to_alias(source, password, dest, marker):
    """One synthetic internal message, authenticated and TLS verified."""
    message = email.message.EmailMessage()
    message['From'] = source
    message['To'] = dest
    message['Subject'] = 'Atoman disposable alias acceptance ' + marker
    message['Message-ID'] = '<' + marker + '@atoman.org>'
    message.set_content('Synthetic alias mail marker: ' + marker + '\n')
    attachment = ('Synthetic alias attachment ' + marker).encode()
    message.add_attachment(attachment, maintype='application', subtype='octet-stream',
                           filename='synthetic.bin')
    with smtplib.SMTP_SSL('mail.atoman.org', 465,
                         context=ssl.create_default_context(), timeout=15) as client:
        client.login(source, password)
        rejected = client.send_message(message, from_addr=source, to_addrs=[dest])
        if rejected:
            raise RuntimeError('Disposable alias SMTP recipient rejected')
    return hashlib.sha256(attachment).hexdigest()


def verify_alias_delivery(owner, password, alias, sender, marker, expected_sha):
    import imaplib
    import email.policy
    with imaplib.IMAP4_SSL('mail.atoman.org', 993,
                          ssl_context=ssl.create_default_context(), timeout=18) as client:
        client.login(owner, password)
        status, _ = client.select('INBOX', readonly=True)
        if status != 'OK':
            raise RuntimeError('Disposable alias owner inbox unavailable')
        for _ in range(24):
            status, data = client.search(None, 'SUBJECT', marker)
            if status != 'OK':
                raise RuntimeError('Disposable alias owner search failed')
            for number in data[0].split():
                ok, rows = client.fetch(number, '(BODY.PEEK[])')
                if ok != 'OK' or not rows or not isinstance(rows[0], tuple):
                    continue
                obj = email.message_from_bytes(rows[0][1], policy=email.policy.default)
                if obj.get('To') != alias or obj.get('From') != sender:
                    continue
                body = obj.get_body(preferencelist=('plain',))
                if marker not in obj.get('Subject', '') or body is None or marker not in body.get_content():
                    raise RuntimeError('Disposable alias message identity mismatch')
                attachments = list(obj.iter_attachments())
                if len(attachments) != 1 or hashlib.sha256(attachments[0].get_payload(decode=True)).hexdigest() != expected_sha:
                    raise RuntimeError('Disposable alias attachment checksum mismatch')
                return
            time.sleep(1)
    raise RuntimeError('Disposable alias mail not delivered to owner inbox')


def run(receipt):
    if receipt.exists() or receipt.is_symlink():
        raise RuntimeError('Refusing to overwrite test receipt')
    base.ensure_internal_tls_destination()
    data = base.admin_credentials(Path('/etc/atoman-mail/backend.env'))
    auth = base64.b64encode((data['ATOMAN_MAIL_STALWART_ADMIN_USER'] + ':' +
                             data['ATOMAN_MAIL_STALWART_ADMIN_PASSWORD']).encode()).decode()
    domain_id = base.verified_domain(auth)
    token = secrets.token_hex(6)
    locals_ = ['opsalias-' + token + '-owner', 'opsalias-' + token + '-sender']
    alias = 'opsalias-' + token + '-extra'
    passwords = [secrets.token_urlsafe(30) for _ in locals_]
    account_ids = []
    steps = []
    error = None
    cleaned = True
    alias_identity_available = None
    try:
        for local, password in zip(locals_, passwords):
            response = base.admin_call(auth, 'x:Account/set',
                                       {'create': {'temporary': base.synthetic_account(local, domain_id, password)}})
            created = response.get('created', {}).get('temporary')
            if not created or not created.get('id') or response.get('notCreated'):
                raise RuntimeError('Temporary alias account provisioning failed')
            account_ids.append(created['id'])
        steps.append('two_disposable_accounts_created')
        owner_id = account_ids[0]
        aliases = base.admin_call(auth, 'x:Account/get',
                                  {'ids': [owner_id], 'properties': ['id', 'aliases']})['list'][0]['aliases']
        if isinstance(aliases, dict):
            slot = max((int(k) for k in aliases if k.isdecimal()), default=-1) + 1
        elif isinstance(aliases, list):
            slot = len(aliases)
        else:
            raise RuntimeError('Unsupported Stalwart alias property format')
        steps.append('alias_remote_shape_' + type(aliases).__name__)
        patch_alias(auth, owner_id, 'aliases/' + str(slot),
                    {'name': alias, 'domainId': domain_id, 'enable': True})
        slot = check_alias(auth, owner_id, alias, domain_id, True)
        steps.append('disposable_alias_created_and_verified')
        to_address = alias + '@' + base.DOMAIN
        owner_address = locals_[0] + '@' + base.DOMAIN
        sender = locals_[1] + '@' + base.DOMAIN
        marker = 'alias' + secrets.token_hex(7)
        attachment_hash = send_to_alias(sender, passwords[1], to_address, marker)
        verify_alias_delivery(owner_address, passwords[0], to_address, sender, marker, attachment_hash)
        steps.append('alias_smtp465_to_primary_imaps993_verified')
        patch_alias(auth, owner_id, 'aliases/' + str(slot) + '/enable', False)
        check_alias(auth, owner_id, alias, domain_id, False)
        steps.append('alias_disable_verified')
        patch_alias(auth, owner_id, 'aliases/' + str(slot) + '/enable', True)
        check_alias(auth, owner_id, alias, domain_id, True)
        steps.append('alias_reenable_verified')
        alias_identity_available = identity_available(to_address, owner_address, owner_id, passwords[0])
        steps.append('native_alias_identity_available' if alias_identity_available
                     else 'native_alias_identity_not_confirmed')
    except Exception as caught:
        error = type(caught).__name__ + ': ' + str(caught)[:165]
    finally:
        for account_id in reversed(account_ids):
            try:
                result = base.admin_call(auth, 'x:Account/set', {'destroy': [account_id]})
                if account_id not in result.get('destroyed', []) or result.get('notDestroyed'):
                    cleaned = False
            except Exception:
                cleaned = False
        steps.append('disposable_accounts_cleaned' if cleaned else 'cleanup_requires_operator_review')
        output = {'format': 'atoman-mail-alias-disposable-acceptance-v1',
                  'completed_at': dt.datetime.now(dt.timezone.utc).isoformat(),
                  'successful': error is None and cleaned,
                  'steps': steps, 'created': len(account_ids),
                  'deleted': len(account_ids) if cleaned else None,
                  'alias_identity_available': alias_identity_available,
                  'personal_accounts_touched': False, 'external_messages_sent': 0,
                  'error': error or (None if cleaned else 'cleanup_unconfirmed'),
                  'remaining_account_ids': [] if cleaned else account_ids}
        receipt.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        fd = os.open(receipt, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        with os.fdopen(fd, 'w') as out:
            json.dump(output, out, indent=2)
            out.flush();os.fsync(out.fileno())
        print(json.dumps({'successful': output['successful'], 'created': len(account_ids),
                          'cleaned': cleaned, 'steps': steps,
                          'error_type': error.split(':', 1)[0] if error else None}), flush=True)
        if not output['successful']:
            raise RuntimeError('Disposable alias acceptance incomplete: inspect private receipt')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--receipt', type=Path, required=True)
    p.add_argument('--confirm-disposable-only', action='store_true')
    a = p.parse_args()
    if not a.confirm_disposable_only:
        p.error('This test is limited to two disposable internal accounts')
    run(a.receipt)
