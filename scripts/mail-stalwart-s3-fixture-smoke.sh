#!/bin/sh
# Fully disposable Stalwart+PostgreSQL+MinIO (S3 test fixture).
# Explicitly NOT a Cloudflare R2 credential test or production deployment.
set -eu
prefix=atoman-mail-s3-smoke
net=$prefix-net
pg=$prefix-pg
blob=$prefix-s3
mail=$prefix-mail
cfgvol=$prefix-config
blobvol=$prefix-data
mailvol=$prefix-maildata
work=$(mktemp -d /tmp/atoman-mail-s3-smoke.XXXXXXXX)
for resource in "$pg" "$blob" "$mail"; do
 if docker container inspect "$resource" >/dev/null 2>&1; then echo 'Staging container already exists; aborting' >&2; rmdir "$work"; exit 2; fi
done
if docker network inspect "$net" >/dev/null 2>&1; then echo 'Staging network already exists; aborting' >&2; rmdir "$work"; exit 2; fi
for v in "$cfgvol" "$blobvol" "$mailvol"; do
 if docker volume inspect "$v" >/dev/null 2>&1; then echo 'Staging volume already exists; aborting' >&2; rmdir "$work"; exit 2; fi
done
cleanup() {
  docker rm -f "$mail" "$blob" "$pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  docker volume rm "$cfgvol" "$blobvol" "$mailvol" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM
pg_password=$(openssl rand -hex 24)
s3_password=$(openssl rand -hex 24)
bootstrap_password=$(openssl rand -hex 24)
export AWS_ACCESS_KEY_ID=stagingtest AWS_SECRET_ACCESS_KEY="$s3_password" AWS_DEFAULT_REGION=us-east-1 AWS_EC2_METADATA_DISABLED=true
bucket=atoman-mail-s3-staging-test
admin_port=18582
smtp_port=19025
imaps_port=19993
s3_port=19000
if ss -lnt | grep -E ":($admin_port|$smtp_port|$imaps_port|$s3_port) " >/dev/null; then echo 'Staging port is occupied' >&2; exit 2; fi
docker network create "$net" >/dev/null
for v in "$cfgvol" "$blobvol" "$mailvol"; do docker volume create "$v" >/dev/null; done
docker run -d --init --name "$pg" --network "$net" --tmpfs /var/lib/postgresql/data:rw,nosuid,nodev,size=384m \
  -e POSTGRES_USER=mail_stage -e POSTGRES_DB=mail_stage -e POSTGRES_PASSWORD="$pg_password" postgres:16-alpine >/dev/null
docker run -d --init --name "$blob" --network "$net" -p 127.0.0.1:$s3_port:9000 -v "$blobvol":/data \
  -e MINIO_ROOT_USER=stagingtest -e MINIO_ROOT_PASSWORD="$s3_password" minio/minio:latest server /data --address :9000 >/dev/null
n=0
until docker exec "$pg" pg_isready -q -U mail_stage -d mail_stage; do
 n=$((n+1)); if [ "$n" -ge 40 ]; then echo 'PG fixture failed' >&2; exit 1; fi; sleep 1
done
n=0
until curl -fsS -o /dev/null -m 2 "http://127.0.0.1:$s3_port/minio/health/ready"; do
 n=$((n+1)); if [ "$n" -ge 40 ]; then echo 'S3 fixture failed' >&2; exit 1; fi; sleep 1
done
aws --endpoint-url "http://127.0.0.1:$s3_port" s3api create-bucket --bucket "$bucket" >/dev/null
python3 - "$work/bootstrap.json" "$pg" "$blob" "$bucket" <<'PY'
import json,sys
path,pg,blob,bucket=sys.argv[1:]
config={
 'defaultHostname':'mail.example.test',
 'defaultDomain':'example.test',
 'requestTlsCertificate':False,
 'generateDkimKeys':False,
 'dnsServer':{'@type':'Manual'},
 'dataStore':{'@type':'PostgreSql','host':pg,'port':5432,'database':'mail_stage','authUsername':'mail_stage','authSecret':{'@type':'EnvironmentVariable','variableName':'MAIL_STAGE_PG_PASSWORD'},'poolMaxConnections':4},
 'blobStore':{'@type':'S3','bucket':bucket,'region':{'@type':'Custom','url':f'http://{blob}:9000','region':'us-east-1'},'accessKeyId':{'@type':'EnvironmentVariable','variableName':'AWS_ACCESS_KEY_ID'},'secretKey':{'@type':'EnvironmentVariable','variableName':'AWS_SECRET_ACCESS_KEY'},'sessionToken':{'@type':'None'}},
 'searchStore':{'@type':'Default'},
 'directory':{'@type':'Internal'},
}
payload={'using':['urn:ietf:params:jmap:core','urn:stalwart:jmap'],'methodCalls':[['x:Bootstrap/set',{'update':{'singleton':config}},'bootstrap']]}
with open(path,'w') as f:json.dump(payload,f)
PY
docker run -d --init --name "$mail" --network "$net" -p 127.0.0.1:$admin_port:8080 -p 127.0.0.1:$smtp_port:25 -p 127.0.0.1:$imaps_port:993 \
  -v "$cfgvol":/etc/stalwart -v "$mailvol":/var/lib/stalwart \
  -e STALWART_RECOVERY_ADMIN="admin:$bootstrap_password" \
  -e MAIL_STAGE_PG_PASSWORD="$pg_password" \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  stalwartlabs/stalwart:v1 >/dev/null
n=0
until curl -fsS -o /dev/null -m 3 "http://127.0.0.1:$admin_port/admin" 2>/dev/null; do
 n=$((n+1)); if [ "$n" -ge 40 ]; then echo 'Stalwart bootstrap HTTP failed' >&2; exit 1; fi; sleep 1
done
curl -fsS -m 50 -u "admin:$bootstrap_password" -H 'Content-Type: application/json' \
  --data-binary @"$work/bootstrap.json" "http://127.0.0.1:$admin_port/jmap/" -o "$work/response.json"
python3 - "$work/response.json" "$work/staging-admin.json" <<'PY'
import json,sys
obj=json.load(open(sys.argv[1]))
responses=obj.get('methodResponses',[])
if not responses:
 print('BOOTSTRAP_RESPONSE_WITHOUT_JMAP_RESULT',str(obj)[:360]);sys.exit(1)
name,data,cid=responses[0]
if name=='error' or data.get('notUpdated') or not data.get('updated'):
 print('BOOTSTRAP_SET_REJECTED',name,json.dumps({'notUpdated':data.get('notUpdated'),'type':data.get('type'),'description':data.get('description')},ensure_ascii=False)[:900]);sys.exit(1)
print('STALWART_BOOTSTRAP_SET_OK')
updated=data.get('updated',{}).get('singleton') or {}
print('BOOTSTRAP_UPDATED_FIELDS',sorted(updated.keys()))
username=updated.get('username')
secret=updated.get('secret')
if isinstance(secret,dict):secret=secret.get('value')
if not isinstance(username,str) or not isinstance(secret,str) or not secret:
 print('BOOTSTRAP_DID_NOT_RETURN_STAGE_ADMIN_CREDENTIAL');sys.exit(1)
with open(sys.argv[2],'w') as fd:json.dump({'username':username,'secret':secret},fd)
import os;os.chmod(sys.argv[2],0o600)
PY
echo '== restart with PostgreSQL + S3 config =='
docker restart "$mail" >/dev/null
n=0
until status=$(curl -sS -o /dev/null -w '%{http_code}' -m 3 "http://127.0.0.1:$admin_port/jmap/" 2>/dev/null) && [ "$status" != "000" ]; do
 n=$((n+1))
 if [ "$(docker inspect "$mail" --format '{{.State.Running}}' 2>/dev/null)" = false ]; then
  echo 'Stalwart post-bootstrap process exited' >&2
  docker logs "$mail" 2>&1 | grep -Ei 'startup failed|error|invalid|failed to' | tail -n 12 >&2 || true
  exit 1
 fi
 if [ "$n" -ge 40 ]; then
  echo 'Stalwart post-bootstrap HTTP unreachable' >&2
  docker logs "$mail" 2>&1 | grep -Ei 'startup failed|error|invalid|failed to' | tail -n 12 >&2 || true
  exit 1
 fi
 sleep 1
done
printf 'POST_BOOTSTRAP_HTTP_STATUS=%s\n' "$status"
schema=$(docker exec "$pg" psql -U mail_stage -d mail_stage -At -c "select count(*) from information_schema.tables where table_schema='public';")
printf 'STALWART_PG_TABLES=%s\n' "$schema"
if [ "$schema" -lt 25 ]; then echo 'PG schema initialization insufficient' >&2; exit 1; fi
printf '%s\n' '== isolated SMTP to local test mailbox =='
python3 - "$smtp_port" <<'SMTPTEST'
import smtplib,sys
from email.message import EmailMessage
msg=EmailMessage()
msg['From']='probe@external.test'
msg['To']='admin@example.test'
msg['Subject']='Stalwart staging object-storage probe'
msg.set_content('Synthetic local-only mail for Atoman test. No production mail data.')
msg.add_attachment(b'staging-file-check',maintype='application',subtype='octet-stream',filename='probe.txt')
try:
 with smtplib.SMTP('127.0.0.1',int(sys.argv[1]),timeout=16) as client:
  client.ehlo('staging.example.test')
  client.send_message(msg)
except Exception as exc:
 print('SYNTHETIC_SMTP_TEST_FAILED',type(exc).__name__,str(exc)[:240]);raise SystemExit(1)
print('SYNTHETIC_SMTP_ACCEPTED')
SMTPTEST
n=0
objects=0
while [ "$n" -lt 25 ]; do
 aws --endpoint-url "http://127.0.0.1:$s3_port" s3api list-objects-v2 --bucket "$bucket" --output json > "$work/blobs.json"
 objects=$(python3 - "$work/blobs.json" <<'PYCOUNT'
import json,sys
x=json.load(open(sys.argv[1]))
print(x.get('KeyCount',len(x.get('Contents',[]))))
PYCOUNT
)
 if [ "$objects" -gt 0 ]; then break; fi
 n=$((n+1)); sleep 1
done
if [ "$objects" -eq 0 ]; then echo 'Stalwart did not persist any S3 blob after synthetic SMTP' >&2; exit 1; fi
printf 'STALWART_STAGED_BLOBS=%s\n' "$objects"
python3 - "$work/blobs.json" "$work" "$bucket" "$s3_port" <<'PYGET'
import json,os,subprocess,sys
manifest,work,bucket,port=sys.argv[1:]
keys=[x['Key'] for x in json.load(open(manifest)).get('Contents',[])]
total=0
for index,key in enumerate(keys):
 target=os.path.join(work,f'downloaded-blob-{index}')
 result=subprocess.run(['aws','--endpoint-url',f'http://127.0.0.1:{port}','s3api','get-object','--bucket',bucket,'--key',key,target],capture_output=True)
 if result.returncode:raise SystemExit(f'Failed S3 GetObject for generated blob #{index}')
 size=os.path.getsize(target)
 if size==0:raise SystemExit(f'Empty generated blob #{index}')
 total+=size
print('STALWART_STAGED_BLOBS_DOWNLOADED',len(keys),'TOTAL_BYTES',total)
PYGET
printf '%s\n' 'STALWART_PG_AND_S3_MESSAGE_PATH_OK'
printf '%s\n' '== restart server after mail acceptance =='
docker restart "$mail" >/dev/null
python3 - "$work/staging-admin.json" "$imaps_port" <<'PYIMAP'
import email,json,imaplib,ssl,sys,time
from email import policy
creds=json.load(open(sys.argv[1]))
context=ssl.create_default_context()
context.check_hostname=False
context.verify_mode=ssl.CERT_NONE
try:
 for attempt in range(25):
  try:
   client=imaplib.IMAP4_SSL('127.0.0.1',int(sys.argv[2]),ssl_context=context,timeout=12)
   break
  except (OSError,ssl.SSLError):
   time.sleep(1)
 else:raise RuntimeError('IMAPS listener did not restart')
 client.login(creds['username'],creds['secret'])
 for attempt in range(16):
  status,_=client.select('INBOX')
  if status!='OK':raise RuntimeError('Cannot select inbox')
  status,data=client.search(None,'ALL')
  if status=='OK' and data and data[0].split():break
  time.sleep(1)
 else:raise RuntimeError('No messages in staging inbox')
 msg_id=data[0].split()[-1]
 status,payload=client.fetch(msg_id,'(RFC822)')
 if status!='OK':raise RuntimeError('Cannot fetch staging message')
 raw=next((t[1] for t in payload if isinstance(t,tuple)),None)
 if raw is None:raise RuntimeError('Missing IMAP body')
 message=email.message_from_bytes(raw,policy=email.policy.default)
 subject=str(message.get('Subject',''))
 if 'Stalwart staging object-storage probe' not in subject:raise RuntimeError('Unexpected staging message subject')
 body=message.get_body(preferencelist=('plain',))
 if body is None or 'Synthetic local-only mail' not in body.get_content():raise RuntimeError('Staging message body not recovered')
 matched=False
 for part in message.walk():
  if part.get_filename()=='probe.txt':
   matched=(part.get_payload(decode=True)==b'staging-file-check')
 if not matched:raise RuntimeError('Staging attachment not recovered')
 print('STAGING_IMAP_MAIL_BODY_AND_ATTACHMENT_SURVIVED_RESTART')
 client.logout()
except Exception as e:
 print('STAGING_IMAP_READ_FAILED',type(e).__name__,str(e)[:200]);sys.exit(1)
PYIMAP
printf '%s\n' 'STALWART_PG_S3_RESTART_RECOVERY_VALIDATED'

# Opt-in stage 5: verify the real JMAP domain/user/API-key/mail read flow
# against this same disposable fixture. No production credentials are read.
if [ "${ATOMAN_MAIL_STAGE5_JMAP:-}" = "1" ]; then
  ATOMAN_MAIL_STAGING_ADMIN_USERNAME=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["username"])' "$work/staging-admin.json")
  ATOMAN_MAIL_STAGING_ADMIN_PASSWORD=$(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["secret"])' "$work/staging-admin.json")
  export ATOMAN_MAIL_STAGING_ADMIN_USERNAME ATOMAN_MAIL_STAGING_ADMIN_PASSWORD
  ATOMAN_MAIL_STAGING_JMAP=1 ATOMAN_MAIL_STAGING_JMAP_URL="http://127.0.0.1:$admin_port" \
    GOMAXPROCS=2 go test ./internal/modules/mail -run '^TestStalwartJMAPRealFixture$' -count=1 -v
  unset ATOMAN_MAIL_STAGING_ADMIN_USERNAME ATOMAN_MAIL_STAGING_ADMIN_PASSWORD
fi
