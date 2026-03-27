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

This is designed to run under `launchd` as a **LaunchAgent**, but route changes require elevated privileges, we need to add `sudo` privileges
for the `/sbin/route` command.

### Configure passwordless sudo for route (recommended)
Create a sudoers drop-in (edit with `visudo` so you don't break sudo):

```bash
sudo visudo -f /etc/sudoers.d/vpn-routes
```

Add a line (replace `YOUR_USERNAME`):

```text
YOUR_USERNAME ALL=(root) NOPASSWD: /sbin/route
```

If you use `--show` and see permissions issues reading routes, also allow:

```text
YOUR_USERNAME ALL=(root) NOPASSWD: /usr/sbin/netstat
```

1) Build and install the binary somewhere stable:

```bash
go build -o ~/bin/vpn-routes main.go
```

2) Copy and edit the plist:
- Start from: `packaging/launchd/vpn-routes.plist`
- Update:
  - binary path
  - `--dev`
  - `--hosts`
  - `--freq`

Then install it as a LaunchAgent:

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
- `/tmp/vpn-routes.out.log`
- `/tmp/vpn-routes.err.log`

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