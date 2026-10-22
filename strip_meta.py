import re, sys, pathlib

KEYS = {"deployed_providers", "deployed_models", "cost_per_1k_input_usd", "tok_per_s"}

def strip(path):
    raw = path.read_bytes()
    nl = '\r\n' if b'\r\n' in raw else '\n'
    text = raw.decode('utf-8')
    lines = text.splitlines(keepends=True)
    out = []
    i = 0
    removed = []
    while i < len(lines):
        line = lines[i]
        m = re.match(r'^([A-Za-z_][A-Za-z0-9_]*):', line)
        if m and m.group(1) in KEYS:
            removed.append(m.group(1))
            i += 1
            while i < len(lines):
                nxt = lines[i]
                if nxt.strip() == "":
                    j = i
                    while j < len(lines) and lines[j].strip() == "":
                        j += 1
                    if j < len(lines) and (lines[j][:1] in (" ", "\t") or lines[j].startswith("- ")):
                        i = j
                        continue
                    break
                if nxt[:1] in (" ", "\t") or nxt.startswith("- "):
                    i += 1
                    continue
                break
            # drop a blank line left dangling directly before the next key
            while out and out[-1].strip() == "" and i < len(lines) and lines[i].strip() != "":
                out.pop()
                break
            continue
        out.append(line)
        i += 1
    # no trailing blank lines, single final newline
    while out and out[-1].strip() == "":
        out.pop()
    text = "".join(out)
    if not text.endswith(nl):
        text += nl
    path.write_bytes(text.encode('utf-8'))
    return removed

for p in sys.argv[1:]:
    print(p, strip(pathlib.Path(p)))
