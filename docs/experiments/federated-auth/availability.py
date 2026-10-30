"""Disposable loopback-only A/B probe. Login and CSRF are intentionally outside this model."""
import hashlib, http.server, json, socket, sqlite3, subprocess, sys, tempfile, time, urllib.error, urllib.request
from pathlib import Path

def connect(path):return sqlite3.connect(path,timeout=2)
def digest(s):return hashlib.sha256(s.encode()).hexdigest()
class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def send(self,status,body):
        data=json.dumps(body).encode();self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(data)));self.end_headers()
        try:self.wfile.write(data)
        except (BrokenPipeError,ConnectionResetError):pass
    def do_GET(self):self.handle_request(False)
    def do_POST(self):self.handle_request(True)
    def handle_request(self,write):
        if role=='a':
            if self.path!='/check' or self.headers.get('Authorization')!='Bearer probe-client-only':return self.send(401,{})
            with connect(adb) as x:
                mode,enabled,generation=x.execute('SELECT mode,enabled,generation FROM account').fetchone()
                x.execute('UPDATE account SET checks=checks+1')
            if mode=='401':return self.send(401,{})
            if mode=='500':return self.send(500,{})
            if mode=='timeout':time.sleep(.4)
            if mode=='malformed':return self.send(200,{'active':'true'})
            return self.send(200,{'active':bool(enabled),'generation':generation,'sub':'wrong' if mode=='wrongsub' else 'subject-1','client':'b'})
        if self.path!='/resource':return self.send(404,{})
        cookie=self.headers.get('Cookie','')
        if cookie!='b_session=probe-browser-token':return self.send(401,{})
        with connect(bdb) as x:
            row=x.execute('SELECT generation FROM sessions WHERE hash=?',(digest('probe-browser-token'),)).fetchone()
        if not row:return self.send(401,{})
        try:
            req=urllib.request.Request(upstream+'/check',headers={'Authorization':'Bearer probe-client-only'})
            with urllib.request.urlopen(req,timeout=.15) as r:
                if r.status!=200:raise ValueError('status')
                reply=json.loads(r.read(2048))
            if type(reply.get('active')) is not bool or reply.get('sub')!='subject-1' or reply.get('client')!='b' or type(reply.get('generation')) is not int:raise ValueError('bad response')
        except (urllib.error.URLError,TimeoutError,ValueError,KeyError):return self.send(503,{})
        if not reply['active'] or reply['generation']!=row[0]:
            with connect(bdb) as x:x.execute('DELETE FROM sessions WHERE hash=?',(digest('probe-browser-token'),))
            return self.send(401,{})
        if write:
            with connect(bdb) as x:x.execute('UPDATE effects SET n=n+1')
        return self.send(200,{'allowed':True})

def main():
    with tempfile.TemporaryDirectory(prefix='availability-',dir=Path(__file__).parent) as temp:
        root=Path(temp);ap=root/'a.sqlite';bp=root/'b.sqlite'
        with connect(ap) as x:x.executescript("CREATE TABLE account(mode TEXT,enabled INTEGER,generation INTEGER,checks INTEGER); INSERT INTO account VALUES('ok',1,1,0);")
        with connect(bp) as x:x.executescript('CREATE TABLE sessions(hash TEXT PRIMARY KEY,generation INTEGER); CREATE TABLE effects(n INTEGER); INSERT INTO effects VALUES(0);')
        def port():
            with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
        aport,bport=port(),port();url=f'http://127.0.0.1:{bport}/resource'
        children=[]
        def req(method='GET'):
            r=urllib.request.Request(url,method=method,headers={'Cookie':'b_session=probe-browser-token'})
            try:
                with urllib.request.urlopen(r,timeout=2) as response:return response.status
            except urllib.error.HTTPError as e:return e.code
        def mode(value):
            with connect(ap) as x:x.execute('UPDATE account SET mode=?',(value,))
        def login():
            with connect(ap) as x:g=x.execute('SELECT generation FROM account').fetchone()[0]
            with connect(bp) as x:x.execute('INSERT OR REPLACE INTO sessions VALUES(?,?)',(digest('probe-browser-token'),g))
        def count():
            with connect(bp) as x:return x.execute('SELECT count(*) FROM sessions').fetchone()[0]
        try:
            for role,p in [('a',aport),('b',bport)]:children.append(subprocess.Popen([sys.executable,__file__,'serve',role,str(p),str(ap),str(bp),f'http://127.0.0.1:{aport}'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL))
            for p in [aport,bport]:
                until=time.monotonic()+5
                while True:
                    try:
                        with socket.create_connection(('127.0.0.1',p),timeout=.1):break
                    except OSError:
                        assert time.monotonic()<until;time.sleep(.03)
            login();assert req()==200;assert req('POST')==200
            with connect(ap) as x:assert x.execute('SELECT checks FROM account').fetchone()[0]==2
            for failure in ['401','500','malformed','wrongsub','timeout']:
                mode(failure);assert req('POST')==503,failure;assert count()==1
                mode('ok');assert req()==200,failure
            with connect(bp) as x:assert x.execute('SELECT n FROM effects').fetchone()[0]==1
            with connect(ap) as x:x.execute('UPDATE account SET enabled=0,generation=generation+1')
            assert req('POST')==401;assert count()==0
            with connect(ap) as x:x.execute('UPDATE account SET enabled=1')
            assert req()==401
            # An unvisited old session also fails after stop/resume, by generation mismatch.
            with connect(bp) as x:x.execute('INSERT INTO sessions VALUES(?,1)',(digest('probe-browser-token'),))
            assert req('POST')==401;assert count()==0
            login();assert req()==200
            children[0].terminate();children[0].wait(timeout=5)
            assert req('POST')==503;assert count()==1
            with connect(bp) as x:assert x.execute('SELECT n FROM effects').fetchone()[0]==1
            print(json.dumps({'case':'A-B-processes-loopback-HTTP','every_request_checked':True,'upstream_401_500_timeout_malformed_wrongsub':'503-session-preserved','upstream_down':'503-session-preserved','stop_and_resume':'401-session-deleted','fresh_login_after_resume':200,'denied_POST_effects':0,'result':'PASS'}))
        finally:
            for child in reversed(children):
                if child.poll() is None:child.terminate()
                try:child.wait(timeout=5)
                except subprocess.TimeoutExpired:child.kill();child.wait()

if __name__=='__main__':
    if len(sys.argv)>1:
        role=sys.argv[2];adb=sys.argv[4];bdb=sys.argv[5];upstream=sys.argv[6]
        http.server.ThreadingHTTPServer(('127.0.0.1',int(sys.argv[3])),Handler).serve_forever()
    else:main()
