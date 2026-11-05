#!/usr/bin/env python3
"""Local-only Stalwart administration. Never prints secrets or mailbox content."""
import argparse,base64,json,os,pathlib,subprocess,time,urllib.request,urllib.error
ROOT=pathlib.Path('/etc/atoman-mail')
BASE='http://127.0.0.1:18580'
def secret_write(path,obj):
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    with os.fdopen(fd,'w') as f:json.dump(obj,f);f.flush();os.fsync(f.fileno())
def credential(bootstrap=False):
    if bootstrap:
        raw=(ROOT/'bootstrap.env').read_text().strip().split('=',1)[1]
        u,p=raw.split(':',1);return {'username':u,'secret':p}
    return json.loads((ROOT/'stalwart-admin.json').read_text())
def request(method,args,bootstrap=False):
    c=credential(bootstrap)
    auth=base64.b64encode((c['username']+':'+c['secret']).encode()).decode()
    body=json.dumps({'using':['urn:ietf:params:jmap:core','urn:stalwart:jmap'],'methodCalls':[[method,args,'c1']]}).encode()
    r=urllib.request.Request(BASE+'/jmap/',data=body,headers={'Authorization':'Basic '+auth,'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(r,timeout=40) as h:out=json.load(h)
    except urllib.error.HTTPError as e:raise RuntimeError('Stalwart HTTP status '+str(e.code)) from None
    responses=out.get('methodResponses',[])
    if len(responses)!=1:raise RuntimeError('Invalid JMAP response shape')
    name,data,_=responses[0]
    if name=='error':raise RuntimeError(method+': '+str(data.get('type')))
    for k in ('notCreated','notUpdated','notDestroyed'):
        if data.get(k):raise RuntimeError(method+': '+k+' '+json.dumps({i:{'type':v.get('type'),'properties':v.get('properties')} for i,v in data[k].items()}))
    return data
def wait():
    for _ in range(45):
        try:
            urllib.request.urlopen(BASE+'/admin',timeout=2).close();return
        except (OSError,urllib.error.URLError):time.sleep(1)
    raise RuntimeError('Stalwart HTTP not ready')
def bootstrap():
    if (ROOT/'stalwart-admin.json').exists():raise RuntimeError('Already bootstrapped; admin credential exists')
    wait()
    b=json.loads((ROOT/'bootstrap-request.json').read_text())['methodCalls'][0][1]
    r=request('x:Bootstrap/set',b,True)
    u=r.get('updated',{}).get('singleton') or {}
    s=u.get('secret')
    if isinstance(s,dict):s=s.get('value') or s.get('secret')
    if not isinstance(u.get('username'),str) or not isinstance(s,str) or not s:raise RuntimeError('Bootstrap returned no admin credential')
    secret_write(ROOT/'stalwart-admin.json',{'username':u['username'],'secret':s})
    print('STALWART_PRODUCTION_BOOTSTRAP_COMPLETE')
def cert():
    c={'certificate':{'@type':'Text','value':pathlib.Path('/etc/letsencrypt/live/mail.atoman.org/fullchain.pem').read_text()},
       'privateKey':{'@type':'Text','secret':pathlib.Path('/etc/letsencrypt/live/mail.atoman.org/privkey.pem').read_text()}}
    ids=request('x:Certificate/query',{'filter':{'subjectAlternativeNames':'mail.atoman.org'}}).get('ids',[])
    if ids:
        request('x:Certificate/set',{'update':{ids[0]:c}});cid=ids[0]
    else:cid=request('x:Certificate/set',{'create':{'mail':c}})['created']['mail']['id']
    request('x:SystemSettings/set',{'update':{'singleton':{'defaultCertificateId':cid}}})
    print('STALWART_TRUSTED_TLS_CERTIFICATE_INSTALLED')
def configure():
    cert()
    listeners=request('x:NetworkListener/get',{'ids':None}).get('list',[])
    if not any(x.get('name')=='submission' for x in listeners):
        request('x:NetworkListener/set',{'create':{'submission':{'name':'submission','protocol':'smtp','bind':{'[::]:587':True},'useTls':True,'implicitTls':False,'maxConnections':128}}})
    request('x:SystemSettings/set',{'update':{'singleton':{'defaultHostname':'mail.atoman.org','maxConnections':1024,'maxBufferedMemory':268435456,'services':{'imap':{'cleartext':False},'smtp':{'cleartext':True},'jmap':{'cleartext':False}},'providerInfo':{'providerName':'Atoman','providerShortName':'Atoman','contactUri':'mailto:support@atoman.org'}}}})
    print('STALWART_PRODUCTION_LISTENERS_CONFIGURED')
def inspect():
    for typ,props in [('Domain',['id','name','dkimManagement','dnsManagement']),('NetworkListener',['id','name','bind','protocol','useTls','implicitTls']),('DkimSignature',['id','selector','publicKey','domainId','stage']),('SystemSettings',['id','defaultHostname','defaultDomainId','defaultCertificateId','services'])]:
        d=request('x:'+typ+'/get',{'ids':['singleton'] if typ=='SystemSettings' else None,'properties':props})
        print(typ,json.dumps(d.get('list',[]),ensure_ascii=False))
def main():
    p=argparse.ArgumentParser();p.add_argument('action',choices=['bootstrap','configure','inspect','renew-certificate']);a=p.parse_args()
    if os.geteuid()!=0:raise RuntimeError('Run as root')
    if a.action=='bootstrap':bootstrap()
    elif a.action=='configure':configure()
    elif a.action=='inspect':inspect()
    else:cert()
if __name__=='__main__':
    try:main()
    except Exception as e:print('MAIL_ADMIN_ERROR',str(e));raise SystemExit(1)
