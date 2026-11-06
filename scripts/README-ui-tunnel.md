# Router UI: viewing the dashboard from your laptop while developing in the VM

The standard workflow (VS Code Remote-SSH docs, JetBrains, Stack Overflow) is:
run the dev server on the VM, then forward the port over your existing SSH
session. Do NOT browse to the VM's LAN IP — this VM sits behind NAT and
`10.39.15.72` is unroutable from a laptop (that's the `ERR_CONNECTION_TIMED_OUT`
you saw). The SSH session you already have is the path.

## Option 1 — VS Code Remote-SSH (what most developers use)

1. On the laptop: install VS Code + the **Remote - SSH** extension.
2. `F1` → **Remote-SSH: Connect to Host...** → your VM → open the
   `aiand_router/frontend` folder.
3. Run `npm run dev` in the integrated terminal. VS Code watches the output and
   **auto-forwards `localhost:3000`** (`remote.autoForwardPorts` is on by
   default); a toast says "open in browser" — or click the `localhost:3000`
   link right in the terminal, which opens VS Code's integrated browser proxied
   over the SSH connection. No manual tunnel command needed.
4. Manual fallback inside VS Code: **Ports** panel → **Forward a Port** → 3000.

## Option 2 — plain SSH forward tunnel (no editor required)

```
ssh -N -L 3000:localhost:3000 <user>@<vm-ssh-address>
```

Open **http://localhost:3000** on the laptop. `-L` pulls the VM's port 3000 to
your laptop over the SSH session; nothing else is exposed.

## Option 3 — reverse tunnel (only if your SSH client blocks `-L`)

```
ssh -N -R 3100:127.0.0.1:3000 <user>@<vm-ssh-address>
```

Then open http://localhost:3100 on the laptop. Useful when a sandbox/VPN client
only lets the laptop *initiate* SSH.

> **Why "it's listening on \*:3000" doesn't help:** `next dev` binds all
> interfaces, but the VM's `10.39.x.x` address lives behind the sandbox's NAT
> gateway and is never routed to your laptop. Browser → VM-IP can never
> connect, no matter what the firewall says.

## VM-side persistent helper (already installed, disabled by default)

`/etc/systemd/system/router-ui-tunnel.service` runs `autossh`, which
continuously re-dials the tunnel if it drops. To use it, point boxd's
`~/.ssh/config` host `router-ui-tunnel` at the laptop's reachable address
first, then:

```
sudo systemctl enable --now router-ui-tunnel
```

## Enable dashboard login (VM side, once)

```
cat >> .env.local <<'EOT'
ROUTER_ADMIN_PASSWORD=<local password>
ROUTER_COOKIE_INSECURE=true
EOT
docker compose up -d server
```

Without `ROUTER_COOKIE_INSECURE=true` the login cookie is marked Secure and is
dropped over plain HTTP, so the dashboard keeps bouncing back to `/ui/login`.

## Checklist when "can't reach this page"

1. `next dev` running in VM?       `pgrep -fa "next dev"`
2. listening on 3000?              `ss -tlnp | grep 3000`
3. tunnel/laptop side alive?       (laptop) `pgrep -fa "ssh.*3000|ssh.*3100"`;
   in VS Code check the **Ports** panel shows 3000 as forwarded
4. VM firewall off?                `sudo ufw status` (inactive is fine)
5. still timing out via VM IP?     expected — use Options 1–3, never the VM IP

## Zero-setup option — Quick Tunnel (used when SSH config is unknown)

If you cannot edit laptop-side SSH config, run on the VM:

    cloudflared tunnel --url http://localhost:3000

It prints a public https://<random>.trycloudflare.com URL; open it in the
laptop browser. No account, no firewall, no SSH config — the VM dials out to
Cloudflare and the browser connects to the same hostname. Downside: the URL
changes every run, and it is public-by-obscurity (fine for local dev, not for
prod). Add /ui/ paths after the hostname; /admin/v1/* still proxies to the
backend inside the VM.
