"""Disposable loopback-only transport and revocation probes. Not an OIDC implementation."""
import base64, hashlib, http.server, json, os, secrets, socket, sqlite3, ssl
import struct, subprocess, sys, tempfile, threading, time, urllib.parse, urllib.request
from pathlib import Path

class Server(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args): pass
    def do_GET(self):
        path = urllib.parse.urlsplit(self.path)
        q = urllib.parse.parse_qs(path.query)
        cookies = self.headers.get('Cookie', '')
        events.append({'path':path.path,'host':self.headers['Host'],'cookie':cookies,
                       'fetch_site':self.headers.get('Sec-Fetch-Site')})
        headers = []
        status = 200
        body = ''
        if path.path == '/events':
            body = json.dumps(events)
        elif path.path == '/admin/seed':
            cookie = 'a_session=alpha; Path=/admin; Secure; HttpOnly; SameSite=Strict' if role == 'a' else 'attempt=beta; Path=/admin; Secure; HttpOnly; SameSite=Lax'
            headers.append(('Set-Cookie',cookie)); body = 'seeded'
        elif path.path == '/start':
            body = '<a id="next" href="'+q['target'][0]+'">login</a>'
        elif path.path == '/admin/entry':
            body = '<p>entry</p><a id="next" href="/admin/continue?'+path.query+'">continue</a>'
        elif path.path == '/admin/continue':
            body = '<p>continue-page</p><a id="next" href="'+q['target'][0]+'">return</a>'
        elif path.path == '/admin/callback':
            mode = q['mode'][0]
            headers.append(('Set-Cookie','b_session=gamma; Path=/admin; Secure; HttpOnly; SameSite='+('Lax' if mode=='lax' else 'Strict')))
            if mode == 'strict200':
                body = '<p>callback</p><a id="next" href="/admin/protected">continue</a>'
            else:
                status = 303; headers.append(('Location','/admin/protected'))
        elif path.path == '/admin/protected': body = 'protected '+cookies
        else: status = 404
        payload=body.encode()
        self.send_response(status)
        for k,v in headers: self.send_header(k,v)
        self.send_header('Content-Type','text/html; charset=utf-8')
        self.send_header('Content-Length',str(len(payload)))
        self.end_headers(); self.wfile.write(payload)

# Minimal CDP transport used only to control the installed browser; no package installation.
class CDP:
    def __init__(self,url):
        u=urllib.parse.urlsplit(url); self.s=socket.create_connection((u.hostname,u.port),timeout=10)
        key=base64.b64encode(secrets.token_bytes(16)).decode()
        self.s.sendall((f'GET {u.path} HTTP/1.1\r\nHost: {u.netloc}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n').encode())
        h=b''
        while not h.endswith(b'\r\n\r\n'): h+=self.s.recv(1)
        assert b'101' in h.split(b'\r\n')[0],h
        self.n=0
    def read(self,n):
        out=b''
        while len(out)<n:
            piece=self.s.recv(n-len(out)); assert piece,'closed'; out+=piece
        return out
    def call(self,method,params=None):
        self.n+=1
        payload=json.dumps({'id':self.n,'method':method,'params':params or {}}).encode()
        mask=secrets.token_bytes(4); n=len(payload)
        head=bytes([129,128+n]) if n<126 else bytes([129,254])+struct.pack('!H',n)
        self.s.sendall(head+mask+bytes(x^mask[i%4] for i,x in enumerate(payload)))
        while True:
            a,b=self.read(2); n=b&127
            if n==126:n=struct.unpack('!H',self.read(2))[0]
            if n==127:n=struct.unpack('!Q',self.read(8))[0]
            p=self.read(n)
            if a&15 != 1:continue
            r=json.loads(p)
            if r.get('id')==self.n:
                assert 'error' not in r,r
                return r.get('result',{})
    def evaluate(self,expr):
        r=self.call('Runtime.evaluate',{'expression':expr,'returnByValue':True})
        return r.get('result',{}).get('value')
    def wait_text(self,needle):
        until=time.monotonic()+8
        while time.monotonic()<until:
            val=self.evaluate('document.body ? document.body.innerText : ""') or ''
            if needle in val:return val
            time.sleep(.03)
        raise AssertionError(('page timeout',needle,self.evaluate('location.href'),val))
    def go(self,url,needle):
        self.call('Page.navigate',{'url':url}); return self.wait_text(needle)
    def click(self,needle):
        self.evaluate('document.getElementById("next").click()'); return self.wait_text(needle)


def browser_probe(root):
    cert=root/'cert.pem'; key=root/'key.pem'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(key),'-out',str(cert),'-days','1','-subj','/CN=probe.test'],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    children=[]
    def port():
        with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
    ap,bp=port(),port()
    context=ssl._create_unverified_context()
    def fetch(p,path):return json.loads(urllib.request.urlopen(f'https://127.0.0.1:{p}{path}',context=context).read())
    try:
        for r,p in [('a',ap),('b',bp)]:
            children.append(subprocess.Popen([sys.executable,__file__,'serve',r,str(p),str(cert),str(key)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL))
        chrome=subprocess.Popen(['chromium','--headless=new','--no-sandbox','--disable-gpu','--no-proxy-server','--disable-background-networking','--disable-component-update','--disable-sync','--no-first-run','--ignore-certificate-errors','--remote-debugging-port=0','--remote-debugging-address=127.0.0.1',f'--user-data-dir={root / "profile"}','--host-resolver-rules=MAP *.test 127.0.0.1','about:blank'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        children.append(chrome)
        active=root/'profile'/'DevToolsActivePort'
        until=time.monotonic()+10
        while not active.exists() and time.monotonic()<until:time.sleep(.05)
        debug_port=active.read_text().splitlines()[0]
        tabs=json.load(urllib.request.urlopen(f'http://127.0.0.1:{debug_port}/json'))
        c=CDP(next(t['webSocketDebuggerUrl'] for t in tabs if t['type']=='page'))
        for same in [False,True]:
            ah='a.example.test' if same else 'a.test'; bh='b.example.test' if same else 'b.test'
            for mode in ['lax','strict303','strict200']:
                c.call('Network.clearBrowserCookies')
                a=f'https://{ah}:{ap}'; b=f'https://{bh}:{bp}'
                c.go(a+'/admin/seed','seeded'); c.go(b+'/admin/seed','seeded')
                callback=b+'/admin/callback?mode='+mode
                entry=a+'/admin/entry?'+urllib.parse.urlencode({'target':callback})
                c.go(b+'/start?'+urllib.parse.urlencode({'target':entry}),'login')
                c.click('entry'); c.click('continue-page')
                ev=fetch(ap,'/events')
                entry_ev=[x for x in ev if x['path']=='/admin/entry'][-1]
                continue_ev=[x for x in ev if x['path']=='/admin/continue'][-1]
                assert ('a_session=alpha' in entry_ev['cookie'])==same,entry_ev
                assert 'a_session=alpha' in continue_ev['cookie'],continue_ev
                if mode=='strict200': c.click('callback');c.click('protected')
                else:c.click('protected')
                ev=fetch(bp,'/events'); cb=[x for x in ev if x['path']=='/admin/callback'][-1]
                protected=[x for x in ev if x['path']=='/admin/protected'][-1]
                assert 'attempt=beta' in cb['cookie'],cb
                has_cookie='b_session=gamma' in protected['cookie']
                assert has_cookie == (mode != 'strict303' or same), protected
                print(json.dumps({'case':'same-site' if same else 'cross-site','mode':mode,'A_entry_cookie':bool(entry_ev['cookie']),'A_continue_cookie':bool(continue_ev['cookie']),'B_attempt_cookie':True,'B_final_cookie':has_cookie}))
        c.s.close()
    finally:
        for child in reversed(children):
            child.terminate()
            try:child.wait(timeout=5)
            except subprocess.TimeoutExpired:child.kill();child.wait()


def revocation_probe(root):
    db=root/'a.sqlite'
    c=sqlite3.connect(db);c.executescript('''PRAGMA journal_mode=WAL;
    CREATE TABLE accounts(id INTEGER PRIMARY KEY, enabled INTEGER, generation INTEGER);
    CREATE TABLE permissions(client TEXT, account INTEGER, allowed INTEGER, generation INTEGER, PRIMARY KEY(client,account));
    CREATE TABLE codes(hash TEXT PRIMARY KEY, account INTEGER, client TEXT, generation INTEGER, permission_generation INTEGER, used INTEGER DEFAULT 0);
    INSERT INTO accounts VALUES(1,1,1),(2,1,1);
    INSERT INTO permissions VALUES('b',1,1,1),('b',2,1,1);
    ''');c.close()
    def connection():return sqlite3.connect(db,timeout=2,isolation_level=None)
    def issue(code):
        with connection() as x:
            x.execute('INSERT INTO codes SELECT ?,id,?,generation,(SELECT generation FROM permissions WHERE client=? AND account=1),0 FROM accounts WHERE id=1 AND enabled=1',(code,'b','b'))
    def exchange(code):
        with connection() as x:
            x.execute('BEGIN IMMEDIATE')
            row=x.execute('''UPDATE codes SET used=1 WHERE hash=? AND used=0
            AND EXISTS(SELECT 1 FROM accounts a JOIN permissions p ON p.account=a.id
            WHERE a.id=codes.account AND a.enabled=1 AND a.generation=codes.generation
            AND p.client=codes.client AND p.allowed=1 AND p.generation=codes.permission_generation)
            RETURNING generation,permission_generation''',(code,)).fetchone()
            x.execute('COMMIT');return row
    def stop_resume():
        with connection() as x:
            x.execute('BEGIN IMMEDIATE');x.execute('UPDATE accounts SET enabled=0,generation=generation+1 WHERE id=1');x.execute('DELETE FROM codes WHERE account=1');x.execute('COMMIT')
            x.execute('UPDATE accounts SET enabled=1 WHERE id=1')
    def active(account,generation,pg):
        with connection() as x:
            return bool(x.execute('SELECT 1 FROM accounts a JOIN permissions p ON p.account=a.id WHERE a.id=? AND a.enabled=1 AND a.generation=? AND p.client=? AND p.allowed=1 AND p.generation=?',(account,generation,'b',pg)).fetchone())
    issue('first'); old=exchange('first');assert old==(1,1);assert active(1,*old)
    issue('pending');stop_resume();assert not active(1,*old);assert exchange('pending') is None;assert active(2,1,1)
    issue('new'); new=exchange('new');assert active(1,*new);assert exchange('new') is None
    with connection() as x:
        x.execute('UPDATE permissions SET allowed=0,generation=generation+1 WHERE account=1');x.execute('UPDATE permissions SET allowed=1 WHERE account=1')
    assert not active(1,*new)
    for i in range(40):
        code='race'+str(i);issue(code);barrier=threading.Barrier(2);result=[];errors=[]
        def worker(fn):
            try:barrier.wait(); result.append(fn())
            except BaseException as e:errors.append(repr(e))
        t1=threading.Thread(target=worker,args=(lambda:exchange(code),));t2=threading.Thread(target=worker,args=(stop_resume,))
        t1.start();t2.start();t1.join();t2.join();assert not errors,errors
        issued=next((r for r in result if r is not None),None)
        assert issued is None or not active(1,*issued),issued
        assert exchange(code) is None
    print(json.dumps({'case':'sqlite-file-WAL','separate_connections':True,'stop_resume_old_session_rejected':True,'pending_code_rejected':True,'permission_regrant_rejected':True,'other_account_unaffected':True,'stop_exchange_races':40,'result':'PASS'}))

if __name__=='__main__':
    if len(sys.argv)>1 and sys.argv[1]=='serve':
        role=sys.argv[2];events=[]
        server=http.server.ThreadingHTTPServer(('127.0.0.1',int(sys.argv[3])),Server)
        context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER);context.load_cert_chain(sys.argv[4],sys.argv[5]);server.socket=context.wrap_socket(server.socket,server_side=True);server.serve_forever()
    else:
        with tempfile.TemporaryDirectory(prefix='probe-',dir=Path(__file__).parent) as tmp:
            root=Path(tmp);browser_probe(root);revocation_probe(root)
