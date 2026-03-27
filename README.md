# vpn-routes

`vpn-routes` is a small macOS daemon that keeps a set of IPv4 host/CIDR routes pointed at a specific VPN interface (e.g. `utun4`), based on a list of hostnames and/or IPs.

## Why would I want it?

Because you need to use OpenVPN to access hosts that change IP regularly and your openvpn client doesn't allow hostname based routes, and you want **only** traffic to those
hosts going through the VPN - i.e. Split Routing. 

...And you are fed up of manually updating the VPN profile with new route entries

## Behavior
- **IPv4 only**
- If the configured interface (via `--dev`) does not exist, the program **no-ops** and does not attempt DNS resolution.
- On macOS, device state is monitored via routing-socket notifications with automatic polling fallback.
- A separate periodic reconcile loop (`--freq`) handles DNS/route convergence and immediately reconciles when it receives a device-up event.
- For hostnames, it resolves **all IPv4 A records** and ensures each resolved IP has a host route via `--dev`.
- It only deletes routes that it previously created (tracked in a state file).
- On shutdown (SIGTERM/SIGINT), it removes all routes it owns.

## Build

```bash
go build -o vpn-routes .
```

## Run (manual)

```bash
sudo ./vpn-routes \
  --dev utun4 \
  --hosts "api.dev.example.com,10.20.30.40,10.0.0.0/24" \
  --freq 30s
```

## Run (JSON config + CLI overrides)

Example `config.json`:

```json
{
  "dev": "utun4",
  "hosts": ["api.dev.example.com", "10.20.30.40", "10.0.0.0/24"],
  "freq": "30s",
  "statePath": "~/Library/Application Support/vpn-routes/state.json",
  "dryRun": false,
  "logLevel": "info"
}
```

Then run:

```bash
sudo ./vpn-routes --config ./config.json --log-level debug
```

### Flags
- **`--config`**: path to JSON config file (loaded first; CLI flags override)
- **`--dev`**: interface name (e.g. `utun4`) (required)
- **`--hosts`**: comma/space-separated list of hostnames, IPv4s, and/or IPv4 CIDRs (required)
- **`--freq`**: reconcile interval (default `30s`)
- **`--state-path`**: where owned-route state is stored (default `/var/db/vpn-routes/state.json`); supports `~/` expansion
- **`--dry-run`**: do not change routes; only log what would be executed
- **`--log-level`**: `debug`, `info`, `warn`, or `error` (default `info`)
- **`--show`**: print current routes using `--dev`, mark which ones are managed, and list desired-but-missing routes, then exit

## launchd (LaunchAgent)

This is designed to run under `launchd` as a **LaunchAgent**. Route changes require elevated privileges, but you don’t have to grant your normal user blanket passwordless `sudo /sbin/route`.

Instead, the recommended setup is:
- run the daemon as your normal GUI user (LaunchAgent)
- have launchd execute the daemon via `sudo -u _vpn_route ...` (a dedicated service user)
- allow `_vpn_route` (and only `_vpn_route`) to run `/sbin/route` as root without a password

### Recommended filesystem layout (`/opt/vpn-routes`)

Create a stable install root:

```bash
sudo mkdir -p /opt/vpn-routes/{bin,logs,state}
sudo chown -R root:wheel /opt/vpn-routes
sudo chmod 0755 /opt/vpn-routes /opt/vpn-routes/bin
sudo chmod 0755 /opt/vpn-routes/logs /opt/vpn-routes/state
```

Place files:
- **binary**: `/opt/vpn-routes/bin/vpn-routes`
- **config**: `/opt/vpn-routes/config.json`
- **state**: `/opt/vpn-routes/state/state.json` (set via `statePath` in config, or `--state-path`)
- **logs**: `/opt/vpn-routes/logs/stdout.log` and `/opt/vpn-routes/logs/stderr.log` (from the plist)

Build and install:

```bash
go build -o vpn-routes .
sudo install -m 0755 vpn-routes /opt/vpn-routes/bin/vpn-routes
```

Example config (note `statePath`):

```json
{
  "dev": "utun4",
  "hosts": ["api.dev.example.com", "10.20.30.40", "10.0.0.0/24"],
  "freq": "30s",
  "statePath": "/opt/vpn-routes/state/state.json",
  "dryRun": false,
  "logLevel": "info"
}
```

Write it to:

```bash
sudo tee /opt/vpn-routes/config.json >/dev/null <<'EOF'
{
  "dev": "utun4",
  "hosts": ["api.dev.example.com", "10.20.30.40", "10.0.0.0/24"],
  "freq": "30s",
  "statePath": "/opt/vpn-routes/state/state.json",
  "dryRun": false,
  "logLevel": "info"
}
EOF
sudo chmod 0644 /opt/vpn-routes/config.json
```

### Create the service user (`_vpn_route`)

Create a dedicated local account with no login shell. Pick an unused UID/GID (example uses `413`).

```bash
sudo dscl . -create /Groups/_vpn_route
sudo dscl . -create /Groups/_vpn_route PrimaryGroupID 413

sudo dscl . -create /Users/_vpn_route
sudo dscl . -create /Users/_vpn_route RealName "vpn-routes service user"
sudo dscl . -create /Users/_vpn_route UniqueID 413
sudo dscl . -create /Users/_vpn_route PrimaryGroupID 413
sudo dscl . -create /Users/_vpn_route NFSHomeDirectory /var/empty
sudo dscl . -create /Users/_vpn_route UserShell /usr/bin/false
sudo dscl . -passwd /Users/_vpn_route '*'
```

Give `_vpn_route` access to state/log folders:

```bash
sudo chown -R _vpn_route:_vpn_route /opt/vpn-routes/logs /opt/vpn-routes/state
sudo chmod 0755 /opt/vpn-routes/logs /opt/vpn-routes/state
```

### Configure `sudoers` (two-step, narrow scope)

Create a sudoers drop-in (edit with `visudo` so you don't break sudo):

```bash
sudo visudo -f /etc/sudoers.d/vpn-routes
```

Add these lines (replace `YOUR_USERNAME`):

```text
YOUR_USERNAME ALL=(_vpn_route) NOPASSWD: /opt/vpn-routes/bin/vpn-routes
_vpn_route ALL=(root) NOPASSWD: /sbin/route
```

Optional (only if you use `--show` and see permissions issues reading routes):

```text
_vpn_route ALL=(root) NOPASSWD: /usr/sbin/netstat
```

### Install the plist

1) Copy and edit the plist:
- Start from: `packaging/launchd/vpn-routes.plist`
- Confirm:
  - `WorkingDirectory` is `/opt/vpn-routes`
  - it runs `/usr/bin/sudo -u _vpn_route ./bin/vpn-routes --config ./config.json`
  - log paths are under `/opt/vpn-routes/logs/`

2) Install it as a LaunchAgent:

```bash
sudo cp packaging/launchd/vpn-routes.plist /Library/LaunchAgents/net.thechriswalker.vpn-routes.plist
sudo launchctl bootstrap gui/$(id -u) /Library/LaunchAgents/net.thechriswalker.vpn-routes.plist
sudo launchctl enable gui/$(id -u)/net.thechriswalker.vpn-routes
sudo launchctl kickstart -k gui/$(id -u)/net.thechriswalker.vpn-routes
```

### Restarting after config changes
If you update the JSON config file or edit arguments in the plist, restart the job:

```bash
sudo launchctl kickstart -k gui/$(id -u)/net.thechriswalker.vpn-routes
```

If you changed the plist file itself, you may need to re-bootstrap it:

```bash
sudo launchctl bootout gui/$(id -u) /Library/LaunchAgents/net.thechriswalker.vpn-routes.plist
sudo launchctl bootstrap gui/$(id -u) /Library/LaunchAgents/net.thechriswalker.vpn-routes.plist
sudo launchctl kickstart -k gui/$(id -u)/net.thechriswalker.vpn-routes
```

To stop/uninstall:

```bash
sudo launchctl bootout gui/$(id -u) /Library/LaunchAgents/net.thechriswalker.vpn-routes.plist
sudo rm /Library/LaunchAgents/net.thechriswalker.vpn-routes.plist
```

Logs (as configured in the plist):
- `/opt/vpn-routes/logs/stdout.log`
- `/opt/vpn-routes/logs/stderr.log`

## Manual verification checklist
- With VPN connected and `--dev` present:
  - `netstat -rn -f inet | grep <ip>` shows host routes via the VPN interface.
  - Changing the DNS A records results in old owned routes being removed and new ones being added.
- With VPN disconnected (interface missing):
  - Program does not modify routes and does not attempt DNS (no DNS-related log spam).
- Restart the agent:
  - Program converges without duplicating routes; stale owned routes are removed.

# AI usage statement.

Almost all of the code here was generated. However it was also reviewed by me, and whilst some of 
it is a bit verbose, or "not the way I would have done it", I just wanted the tool so I left it
as is. 

If you find that scary - especially as it needs to run as root to update the routing table, then
this may not be for you.