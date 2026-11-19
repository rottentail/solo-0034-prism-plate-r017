#!/usr/bin/env python3
"""Run bounded production HTTP journeys against an isolated ephemeral service."""
import argparse, concurrent.futures, copy, hashlib, json, os, pathlib, signal, socket, struct, subprocess, tempfile, time, urllib.error, urllib.request
ROOT=pathlib.Path(__file__).resolve().parents[1]
def png_pixels(raw):
    import zlib
    assert raw[:8]==b'\x89PNG\r\n\x1a\n'
    pos=8;idat=b'';w=h=None;bpp=None
    while pos<len(raw):
        n=struct.unpack('>I',raw[pos:pos+4])[0];kind=raw[pos+4:pos+8];chunk=raw[pos+8:pos+8+n];pos+=12+n
        if kind==b'IHDR':
            w,h=struct.unpack('>II',chunk[:8]);depth,ctype=chunk[8],chunk[9]
            bpp={2:3,6:4}[ctype];assert depth==8
        elif kind==b'IDAT':idat+=chunk
        elif kind==b'IEND':break
    data=zlib.decompress(idat);stride=w*bpp;out=[];prev=bytearray(stride);i=0
    for _ in range(h):
        f=data[i];i+=1;line=bytearray(data[i:i+stride]);i+=stride
        for x in range(stride):
            a=line[x-bpp] if x>=bpp else 0;b=prev[x];c=prev[x-bpp] if x>=bpp else 0
            if f==1:line[x]=(line[x]+a)&255
            elif f==2:line[x]=(line[x]+b)&255
            elif f==3:line[x]=(line[x]+((a+b)//2))&255
            elif f==4:
                p=a+b-c;pa,pb,pc=abs(p-a),abs(p-b),abs(p-c)
                line[x]=(line[x]+(a if pa<=pb and pa<=pc else b if pb<=pc else c))&255
        out.append(bytes(line));prev=line
    return w,h,bpp,out
def component_workflow(call,render,seed,base,start,stop):
    bg={'r':0,'g':0,'b':0,'a':1}
    doc=copy.deepcopy(seed['document']);doc['width']=96;doc['height']=72
    src={'id':'badge-src','title':'徽记源稿','document':doc}
    call('POST','/v1/plates',src,status=201)
    def plate(pid,layers,width=128,height=96):
        return {'id':pid,'title':pid,'document':{'width':width,'height':height,'background':bg,'layers':layers}}
    def mount(lid,matrix,plate='badge-src',revision=1):
        return {'id':lid,'opacity':1,'blend':'normal','component':{'plate':plate,'revision':revision,'transform':matrix}}
    # Two mounts of one frozen revision, at different scales and offsets.
    two=plate('badge-plate',[mount('badge-a',[1,0,0,1,0,0]),mount('badge-b',[0.5,0,0,0.5,4,3])])
    created=call('POST','/v1/plates',two,status=201)
    digest=created['revisions'][0]['document']['layers'][0]['component']['digest']
    assert created['revisions'][0]['document']['layers'][1]['component']['digest']==digest
    a=call('POST','/v1/plates/badge-plate/render?revision=1&format=png',status=201)
    raw=call('GET','/v1/artifacts/'+a['id'],raw=True)[0];_,_,bpp,before=png_pixels(raw)
    # The source plate gains a revision; the already delivered plate must not move.
    changed=copy.deepcopy(doc);changed['background']={'r':1,'g':1,'b':1,'a':1}
    call('PUT','/v1/plates/badge-src',{'expected_revision':1,'document':changed})
    again=call('POST','/v1/plates/badge-plate/render?revision=1&format=png',status=201)
    assert again['id']==a['id']
    assert call('GET','/v1/artifacts/'+a['id'],raw=True)[0]==raw
    # A mounted instance is composited locally, then transformed into the plate.
    plain=plate('badge-plain',[mount('badge-p',[1,0,0,1,0,0])],96,72)
    call('POST','/v1/plates',plain,status=201)
    p=call('POST','/v1/plates/badge-plain/render?revision=1&format=png',status=201)
    _,_,_,pix=png_pixels(call('GET','/v1/artifacts/'+p['id'],raw=True)[0])
    assert pix!=before
    # Mirroring keeps the child's local clip and gradient: the mirrored result is
    # exactly the horizontal reflection of the unmounted instance.
    mirror=plate('badge-mirror',[mount('badge-m',[-1,0,0,1,96,0])],96,72)
    call('POST','/v1/plates',mirror,status=201)
    m=call('POST','/v1/plates/badge-mirror/render?revision=1&format=png',status=201)
    _,_,_,mpx=png_pixels(call('GET','/v1/artifacts/'+m['id'],raw=True)[0])
    assert mpx!=pix
    # Mirroring is a pure reflection of the child's local content and clip.
    assert all(mpx[y][x*bpp:(x+1)*bpp]==pix[y][(95-x)*bpp:(96-x)*bpp] for y in range(72) for x in range(96))
    # SVG is self-contained: no source-plate name leaks, and each mount owns ids.
    svg=call('POST','/v1/plates/badge-plate/render?revision=1&format=svg',status=201)
    svgraw=call('GET','/v1/artifacts/'+svg['id'],raw=True)[0]
    assert b'badge-src' not in svgraw and svgraw.count(b'<linearGradient')>=2 and b'matrix(' in svgraw
    ids=[chunk.split(b'"')[0] for chunk in svgraw.split(b'id="')[1:]]
    assert len(ids)==len(set(ids))
    # Scaling the mount leaves child geometry in child coordinates: the path,
    # clip and gradient axis strings are byte-identical, only the outer matrix
    # differs. This is what keeps a nested instance's local meaning intact.
    def svg_of(pid):
        art=call('POST',f'/v1/plates/{pid}/render?revision=1&format=svg',status=201)
        return call('GET','/v1/artifacts/'+art['id'],raw=True)[0]
    call('POST','/v1/plates',plate('badge-scaled',[mount('badge-s',[0.25,0,0,0.25,7,5])],128,96),status=201)
    def local_parts(raw):
        import re
        return sorted(re.findall(rb'(?:d|x1|y1|x2|y2)="[^"]*"',raw))
    assert local_parts(svg_of('badge-plain'))==local_parts(svg_of('badge-scaled'))
    assert svg_of('badge-plain')!=svg_of('badge-scaled')
    # Revising a plate may add or swap a component mount; the new revision pins
    # the same frozen source as revision 1 of the source plate did.
    call('POST','/v1/plates',{'id':'badge-plain2','title':'平版2','document':plain['document']},status=201)
    call('PUT','/v1/plates/badge-plain2',{'expected_revision':1,'document':{'width':128,'height':96,'background':bg,'layers':[mount('badge-a',[1,0,0,1,0,0]),mount('badge-b',[0.5,0,0,0.5,4,3])]}},status=200)
    more=call('GET','/v1/plates/badge-plain2')
    assert more['revisions'][1]['document']['layers'][0]['component']['revision']==1
    r2=call('POST','/v1/plates/badge-plain2/render?revision=2&format=png',status=201)
    assert r2['digest']==a['digest']  # same frozen content, different plate identity
    # Branching copies the frozen digest; the fork keeps no live dependency.
    fork=call('POST','/v1/plates/badge-plain2/branch',{'revision':2,'id':'badge-fork','title':'分支'},status=201)
    assert fork['revisions'][0]['document']['layers'][0]['component']['digest']==digest
    fork_seal,_=call('GET','/v1/plates/badge-fork/bundle',raw=True)
    assert b'"components"' in fork_seal
    # A fresh plate may mount that frozen digest directly, without a source name.
    call('POST','/v1/plates',{'id':'badge-digest-only','title':'仅摘要','document':{'width':96,'height':72,'background':bg,'layers':[{'id':'d','opacity':1,'blend':'normal','component':{'digest':digest,'transform':[1,0,0,1,0,0]}}]}},status=201)
    d1=call('POST','/v1/plates/badge-digest-only/render?revision=1&format=png',status=201)
    assert d1['digest']==p['digest']
    unknown=copy.deepcopy(plate('badge-unknown',[mount('u',[1,0,0,1,0,0])],32,32))
    unknown['document']['layers'][0]['component']={'digest':'0'*64,'transform':[1,0,0,1,0,0]}
    call('POST','/v1/plates',unknown,status=404)
    # A mount that points at the very revision being appended is an explicit cycle.
    call('PUT','/v1/plates/badge-plain2',{'expected_revision':2,'document':{'width':32,'height':32,'background':bg,'layers':[mount('self',[1,0,0,1,0,0],plate='badge-plain2',revision=3)]}},status=400)
    assert len(call('GET','/v1/plates/badge-plain2')['revisions'])==2
    # The seal carries the frozen closure so a fresh instance reproduces the image.
    sealed,_=call('GET','/v1/plates/badge-plate/bundle',raw=True);payload=json.loads(sealed)
    assert digest in payload['components'] and payload['plate']['id']=='badge-plate'
    stop();os.rename(base/'state',base/'original');start()
    # The receiver has its own badge-src with different content; import must rely
    # on the sealed closure, never on a same-named local plate.
    call('POST','/v1/plates',{'id':'badge-src','title':'本地同名','document':changed},status=201)
    imported=call('POST','/v1/bundles',sealed,status=201)
    assert imported['id']=='badge-plate'
    assert call('POST','/v1/plates/badge-plate/render?revision=1&format=png',status=201)['digest']==a['digest']
    # A branch sealed before the restart also lands on a receiver without its source.
    forked=call('POST','/v1/bundles',fork_seal,status=201)
    assert forked['id']=='badge-fork'
    assert call('POST','/v1/plates/badge-fork/render?revision=1&format=png',status=201)['digest']==r2['digest']
    # A mount of the revision currently being written is an explicit cycle.
    cyc=plate('badge-cycle',[mount('self-mount',[1,0,0,1,0,0],plate='badge-cycle',revision=1)],32,32)
    call('POST','/v1/plates',cyc,status=400);call('GET','/v1/plates/badge-cycle',status=404)
    # A future source revision and a built-in digest cycle are rejected the same way.
    fut=plate('badge-future',[mount('future',[1,0,0,1,0,0],plate='badge-src',revision=9)],32,32)
    call('POST','/v1/plates',fut,status=404)
    # Missing source and an over-large expanded graph never enter history.
    miss=plate('badge-missing',[mount('miss',[1,0,0,1,0,0],plate='absent-plate')],32,32)
    call('POST','/v1/plates',miss,status=404);call('GET','/v1/plates/badge-missing',status=404)
    wide=copy.deepcopy(doc)
    wide['layers']=[{'id':f'w{i}','opacity':1,'blend':'normal','shapes':[{'id':f'w{i}s{j}','fill_rule':'nonzero',
        'path':[{'kind':'M','points':[{'x':0,'y':0}]},{'kind':'L','points':[{'x':8,'y':0}]},{'kind':'L','points':[{'x':0,'y':8}]},{'kind':'Z'}],
        'transform':[1,0,0,1,0,0],'brush':{'kind':'solid','color':{'r':.5,'g':.5,'b':.5,'a':1}}} for j in range(32)]} for i in range(16)]
    call('POST','/v1/plates',{'id':'badge-fan','title':'扇','document':wide},status=201)
    fan=plate('badge-wide',[mount(f'fan{i}',[1,0,0,1,0,0],plate='badge-fan') for i in range(16)],32,32)
    call('POST','/v1/plates',fan,status=400);call('GET','/v1/plates/badge-wide',status=404)
    # Nesting is capped: badge-src plus seven mounts is the deepest admitted chain.
    prev='badge-src'
    for i in range(1,7):
        call('POST','/v1/plates',plate(f'badge-chain-{i}',[mount('c',[1,0,0,1,0,0],plate=prev)],32,32),status=201)
        prev=f'badge-chain-{i}'
    call('POST','/v1/plates',plate('badge-chain-7',[mount('c',[1,0,0,1,0,0],plate=prev)],32,32),status=201)
    call('POST','/v1/plates',plate('badge-chain-8',[mount('c',[1,0,0,1,0,0],plate='badge-chain-7')],32,32),status=400)
    call('GET','/v1/plates/badge-chain-8',status=404)
    # Tampered component payloads are rejected at the seal boundary.
    bad=copy.deepcopy(payload);bad['components'][digest]['width']=1
    call('POST','/v1/bundles',bad,status=400)
    bad=copy.deepcopy(payload);bad['components']['0'*64]=bad['components'][digest]
    call('POST','/v1/bundles',bad,status=400)
def main():
    parser=argparse.ArgumentParser();parser.add_argument('--workflow',required=True);args=parser.parse_args()
    seed=json.loads((ROOT/'examples/amber-proof.json').read_text())
    with tempfile.TemporaryDirectory(prefix='prism-plate-') as tmp:
        base=pathlib.Path(tmp);binary=base/'prismplate';subprocess.run(['go','build','-o',str(binary),'./cmd/prismplate'],cwd=ROOT,check=True)
        sock=socket.socket();sock.bind(('127.0.0.1',0));port=sock.getsockname()[1];sock.close();url=f'http://127.0.0.1:{port}'
        log=(base/'service.log').open('w');proc=None
        def start():
            nonlocal proc
            proc=subprocess.Popen([str(binary),'-listen',f'127.0.0.1:{port}','-data',str(base/'state')],stdout=log,stderr=log)
            for _ in range(100):
                if proc.poll() is not None:raise RuntimeError('service exited')
                try:
                    urllib.request.urlopen(url+'/healthz',timeout=1).close();return
                except OSError:time.sleep(.05)
            raise RuntimeError('startup timed out')
        def stop():
            if proc and proc.poll() is None:
                proc.send_signal(signal.SIGTERM)
                try:proc.wait(timeout=45)
                except subprocess.TimeoutExpired:proc.kill();proc.wait();raise
        def call(method,path,data=None,status=200,raw=False,headers=None):
            body=data if isinstance(data,bytes) else (json.dumps(data).encode() if data is not None else None)
            req=urllib.request.Request(url+path,data=body,method=method,headers=headers or {'Content-Type':'application/json'})
            try:
                with urllib.request.urlopen(req,timeout=35) as r:code=r.status;payload=r.read();hs={k.lower():v for k,v in r.headers.items()}
            except urllib.error.HTTPError as e:code=e.code;payload=e.read();hs={k.lower():v for k,v in e.headers.items()}
            if code!=status:raise RuntimeError(f'{method} {path}: expected {status}, observed {code}: {payload[:200]!r}')
            return (payload,hs) if raw else json.loads(payload) if payload else None
        def render(fmt='png',revision=1):return call('POST',f'/v1/plates/amber-plate/render?revision={revision}&format={fmt}',status=201)
        try:
            start();created=call('POST','/v1/plates',seed,status=201);assert created['revisions'][0]['number']==1
            w=args.workflow
            if w=='create':
                assert call('GET','/v1/plates/amber-plate')['title']==seed['title'];call('POST','/v1/plates',seed,status=409)
                bad=copy.deepcopy(seed);bad['id']='bad-plate';bad['document']['layers'][0]['shapes'][0]['transform']=[0]*6;call('POST','/v1/plates',bad,status=400)
            elif w=='revise':
                q={'expected_revision':1,'document':copy.deepcopy(seed['document'])};q['document']['background']['r']=.2
                with concurrent.futures.ThreadPoolExecutor(2) as pool:
                    def attempt(_):
                        try:return call('PUT','/v1/plates/amber-plate',q)
                        except RuntimeError as e:
                            if 'observed 409' in str(e):return 'conflict'
                            raise
                    results=list(pool.map(attempt,range(2)))
                assert sum(x=='conflict' for x in results)==1;assert len(call('GET','/v1/plates/amber-plate')['revisions'])==2
                assert call('GET','/v1/plates/amber-plate')['revisions'][0]['document']==created['revisions'][0]['document']
            elif w=='branch':
                b=call('POST','/v1/plates/amber-plate/branch',{'revision':1,'id':'amber-fork','title':'琥珀新稿'},status=201);assert b['revisions'][0]['digest']==created['revisions'][0]['digest'];call('POST','/v1/plates/amber-plate/branch',{'revision':99,'id':'missing-fork','title':'无效稿'},status=404)
            elif w=='archive':
                call('POST','/v1/plates/amber-plate/archive');call('PUT','/v1/plates/amber-plate',{'expected_revision':1,'document':seed['document']},status=409);assert render()['revision']==1
            elif w in ('png','svg','artifact'):
                fmt='svg' if w=='svg' else 'png';a=render(fmt);raw,hs=call('GET','/v1/artifacts/'+a['id'],raw=True);assert hashlib.sha256(raw).hexdigest()==a['digest']
                if fmt=='png':assert raw[:8]==b'\x89PNG\r\n\x1a\n';assert struct.unpack('>II',raw[16:24])==(192,144)
                else:assert b'<svg' in raw and b'<linearGradient' in raw and b'clipPath' in raw
                assert render(fmt)['digest']==a['digest'];call('GET','/v1/artifacts/'+a['id'],status=304,raw=True,headers={'If-None-Match':hs['etag']});call('GET','/v1/artifacts/missing',status=404)
                if w=='artifact':call('DELETE','/v1/artifacts/'+a['id']);call('GET','/v1/artifacts/'+a['id'],status=409)
            elif w in ('exposure','cancel'):
                job=call('POST','/v1/plates/amber-plate/exposures?revision=1&format=png',status=202)
                if w=='cancel':call('DELETE','/v1/exposures/'+job['id'],status=202)
                for _ in range(200):
                    job=call('GET','/v1/exposures/'+job['id'])
                    if job['state'] in ('succeeded','cancelled','failed'):break
                    time.sleep(.02)
                assert job['state'] in (('succeeded','cancelled') if w=='cancel' else ('succeeded',))
                if job['state']=='succeeded':assert call('GET','/v1/artifacts/'+job['artifact'],raw=True)[0][:8]==b'\x89PNG\r\n\x1a\n'
                call('DELETE','/v1/exposures/absent',status=404)
            elif w=='proof':
                a=render();call('POST','/v1/proofs',{'id':'proof-amber','artifact':a['id']},status=201);assert call('PUT','/v1/proofs/proof-amber',{'state':'accepted','note':'叠色与镂空检查通过'})['state']=='accepted';call('DELETE','/v1/artifacts/'+a['id'],status=409);call('PUT','/v1/proofs/proof-amber',{'state':'rejected','note':'重复裁决'},status=409)
            elif w in ('bundle','restore'):
                a=render();sealed,_=call('GET','/v1/plates/amber-plate/bundle',raw=True);payload=json.loads(sealed);assert payload['plate']['revisions'][0]['digest']==a['scene_digest']
                if w=='bundle':
                    call('POST','/v1/bundles',sealed,status=409);payload['digest']='0'*64;call('POST','/v1/bundles',payload,status=400)
                    stop();os.rename(base/'state',base/'original');start();imported=call('POST','/v1/bundles',sealed,status=201);assert imported['revisions'][0]['digest']==a['scene_digest'];assert render()['digest']==a['digest']
                else:
                    stop();start();assert call('GET','/v1/artifacts/'+a['id'],raw=True)[0][:8]==b'\x89PNG\r\n\x1a\n';assert render()['digest']==a['digest']
            elif w=='isolation':
                lockproc=subprocess.run([str(binary),'-listen','127.0.0.1:0','-data',str(base/'state')],capture_output=True,timeout=3);assert lockproc.returncode!=0;assert b'locked' in lockproc.stderr
                call('POST','/v1/plates',b'{"id":"x"} true',status=400)
            elif w=='rejection':
                bad=copy.deepcopy(seed);bad['id']='oversize';bad['document']['width']=513;call('POST','/v1/plates',bad,status=400);call('GET','/v1/plates/oversize',status=404)
                bad=copy.deepcopy(seed);bad['id']='invalid-brush';bad['document']['layers'][0]['shapes'][0]['brush']['stops'][1]['at']=0;call('POST','/v1/plates',bad,status=400);assert render()['revision']==1
            elif w=='component':
                component_workflow(call,render,seed,base,start,stop)
            else:raise RuntimeError('unknown workflow '+w)
            print(json.dumps({'workflow':w,'result':'passed','entry':'HTTP','port':port},ensure_ascii=False))
        finally:stop();log.close()
if __name__=='__main__':main()
