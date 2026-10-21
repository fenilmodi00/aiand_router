import os, re, subprocess, sys
root='D:/router-chop-adapters'
def vet(pkg):
    r=subprocess.run(['go','vet',pkg],cwd=root,capture_output=True,text=True)
    return r.stdout+r.stderr
pat=re.compile(r'^(?:vet\.exe: )?([A-Za-z0-9_\\\/.]+\.go):(\d+):\d+: "([^"]+)" imported and not used')
for rounds in range(60):
    out=vet('./internal/proxy/')
    byfile={}
    for line in out.split('\n'):
        m=pat.match(line.strip())
        if m:
            rel=m.group(1).replace('\\','/')
            if rel.startswith(('internal/','cmd/','scripts/')):
                byfile.setdefault(rel,set()).add(m.group(3))
    if not byfile:
        print("CLEAN"); break
    for rel, imps in byfile.items():
        p=os.path.join(root,rel)
        raw=open(p,encoding='utf-8',errors='replace').read()
        nl='\r\n' if '\r\n' in raw else '\n'
        lines=raw.replace('\r\n','\n').split('\n')
        out2=[]
        for l in lines:
            st=l.strip()
            if st.startswith('"') and st.rstrip(',').strip('"') in imps:
                continue
            out2.append(l)
        open(p,'w',encoding='utf-8',newline='').write(nl.join(out2))
    print("round",rounds,len(byfile), flush=True)
