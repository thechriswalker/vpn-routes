package routes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const routePath = "/sbin/route"
const netstatPath = "/usr/sbin/netstat"
const sudoPath = "/usr/bin/sudo"

type Runner struct {
	timeout time.Duration
	dryRun  bool
	log     *slog.Logger
}

func New(timeout time.Duration, dryRun bool, logger *slog.Logger) *Runner {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Runner{timeout: timeout, dryRun: dryRun, log: logger}
}

func (r *Runner) EnsureHostRoute(ctx context.Context, dev string, ip net.IP) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("not an ipv4 address: %v", ip)
	}

	ok, err := r.hostRouteUsesDev(ctx, ip4.String(), dev)
	if err == nil && ok {
		return nil
	}

	if r.dryRun {
		r.printf("dry-run: %s -n %s -n add -host %s -interface %s", sudoPath, routePath, ip4.String(), dev)
		return nil
	}

	// Best-effort add; ignore “already exists”.
	out, runErr := r.run(ctx, "-n", "add", "-host", ip4.String(), "-interface", dev)
	if runErr != nil {
		if isAlreadyExists(out) {
			return nil
		}
		return fmt.Errorf("route add host %s via %s: %w (out=%q)", ip4.String(), dev, runErr, out)
	}
	return nil
}

func (r *Runner) EnsureNetRoute(ctx context.Context, dev string, ipnet net.IPNet) error {
	if ipnet.IP.To4() == nil {
		return fmt.Errorf("not an ipv4 network: %s", ipnet.String())
	}

	ok, err := r.netRouteUsesDev(ctx, ipnet.String(), dev)
	if err == nil && ok {
		return nil
	}

	if r.dryRun {
		r.printf("dry-run: %s -n %s -n add -net %s -interface %s", sudoPath, routePath, ipnet.String(), dev)
		return nil
	}

	out, runErr := r.run(ctx, "-n", "add", "-net", ipnet.String(), "-interface", dev)
	if runErr != nil {
		if isAlreadyExists(out) {
			return nil
		}
		return fmt.Errorf("route add net %s via %s: %w (out=%q)", ipnet.String(), dev, runErr, out)
	}
	return nil
}

func (r *Runner) DeleteHostRoute(ctx context.Context, ip net.IP, dev string) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("not an ipv4 address: %v", ip)
	}

	if r.dryRun {
		r.printf("dry-run: %s -n %s -n delete -host %s -interface %s", sudoPath, routePath, ip4.String(), dev)
		return nil
	}

	out, runErr := r.run(ctx, "-n", "delete", "-host", ip4.String(), "-interface", dev)
	if runErr != nil {
		if isNotFound(out) {
			return nil
		}
		// Some macOS versions don’t accept -interface for delete; fall back.
		out2, runErr2 := r.run(ctx, "-n", "delete", "-host", ip4.String())
		if runErr2 != nil {
			if isNotFound(out2) {
				return nil
			}
			return fmt.Errorf("route delete host %s: %w (out=%q) (out2=%q)", ip4.String(), runErr2, out, out2)
		}
	}
	return nil
}

func (r *Runner) DeleteNetRoute(ctx context.Context, ipnet net.IPNet, dev string) error {
	if ipnet.IP.To4() == nil {
		return fmt.Errorf("not an ipv4 network: %s", ipnet.String())
	}

	if r.dryRun {
		r.printf("dry-run: %s -n %s -n delete -net %s -interface %s", sudoPath, routePath, ipnet.String(), dev)
		return nil
	}

	out, runErr := r.run(ctx, "-n", "delete", "-net", ipnet.String(), "-interface", dev)
	if runErr != nil {
		if isNotFound(out) {
			return nil
		}
		out2, runErr2 := r.run(ctx, "-n", "delete", "-net", ipnet.String())
		if runErr2 != nil {
			if isNotFound(out2) {
				return nil
			}
			return fmt.Errorf("route delete net %s: %w (out=%q) (out2=%q)", ipnet.String(), runErr2, out, out2)
		}
	}
	return nil
}

func (r *Runner) hostRouteUsesDev(ctx context.Context, ip string, dev string) (bool, error) {
	out, err := r.run(ctx, "-n", "get", ip)
	if err != nil {
		return false, err
	}
	return parseInterface(out) == dev, nil
}

func (r *Runner) netRouteUsesDev(ctx context.Context, cidr string, dev string) (bool, error) {
	out, err := r.run(ctx, "-n", "get", "-net", cidr)
	if err != nil {
		return false, err
	}
	return parseInterface(out) == dev, nil
}

func (r *Runner) HostRouteUsesDev(ctx context.Context, ip string, dev string) (bool, error) {
	return r.hostRouteUsesDev(ctx, ip, dev)
}

func (r *Runner) NetRouteUsesDev(ctx context.Context, cidr string, dev string) (bool, error) {
	return r.netRouteUsesDev(ctx, cidr, dev)
}

// ListDestinationsByDev lists destinations from the IPv4 routing table whose Netif matches dev.
// Destinations are returned as they appear in `netstat -rn -f inet` (often an IP for host routes
// or a CIDR-ish string for network routes).
func (r *Runner) ListDestinationsByDev(ctx context.Context, dev string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, netstatPath, "-rn", "-f", "inet")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		return nil, fmt.Errorf("netstat failed: %w (out=%q)", err, out)
	}

	lines := strings.Split(out, "\n")
	netifIdx := -1
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Destination") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "Netif" {
					netifIdx = i
					break
				}
			}
			break
		}
	}
	if netifIdx == -1 {
		return nil, fmt.Errorf("could not find Netif column in netstat output")
	}

	seen := map[string]struct{}{}
	var dests []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Routing tables") || strings.HasPrefix(line, "Internet:") || strings.HasPrefix(line, "Destination") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) <= netifIdx {
			continue
		}
		if fields[netifIdx] != dev {
			continue
		}
		dest := fields[0]
		if dest == "" {
			continue
		}
		if _, ok := seen[dest]; ok {
			continue
		}
		seen[dest] = struct{}{}
		dests = append(dests, dest)
	}

	sort.Strings(dests)
	return dests, nil
}

func (r *Runner) run(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// Use sudo in non-interactive mode so the service never hangs on a password prompt.
	// This requires either (a) running as root, or (b) sudoers NOPASSWD for /sbin/route.
	cmdArgs := append([]string{"-n", routePath}, args...)
	cmd := exec.CommandContext(cctx, sudoPath, cmdArgs...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		if strings.Contains(out, "a password is required") {
			return out, fmt.Errorf("sudo requires a password (configure NOPASSWD for %s): %w", routePath, err)
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			return out, fmt.Errorf("route command timeout: %w", err)
		}
		return out, err
	}
	return out, nil
}

func parseInterface(out string) string {
	// `route -n get ...` output contains a line like: "interface: utun4"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return ""
}

func isAlreadyExists(out string) bool {
	o := strings.ToLower(out)
	return strings.Contains(o, "file exists") || strings.Contains(o, "already in table")
}

func isNotFound(out string) bool {
	o := strings.ToLower(out)
	return strings.Contains(o, "not in table") || strings.Contains(o, "no such process")
}

func (r *Runner) printf(format string, args ...any) {
	if r.log == nil {
		return
	}
	r.log.Info(fmt.Sprintf(format, args...), "dry_run", r.dryRun)
}

