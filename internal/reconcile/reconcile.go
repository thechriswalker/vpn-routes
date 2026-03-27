package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"vpn-routes/internal/config"
	"vpn-routes/internal/netif"
	"vpn-routes/internal/resolve"
	"vpn-routes/internal/routes"
	"vpn-routes/internal/state"
	"vpn-routes/internal/targets"
)

type Reconciler struct {
	cfg    config.Config
	store  state.Store
	log    *slog.Logger
	res    *resolve.Resolver
	routes *routes.Runner
}

func New(cfg config.Config, store state.Store, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		cfg:    cfg,
		store:  store,
		log:    logger,
		res:    resolve.New(5 * time.Second),
		routes: routes.New(5*time.Second, cfg.DryRun, logger),
	}
}

func (r *Reconciler) Tick(ctx context.Context) error {
	if !netif.Exists(r.cfg.Dev) {
		r.log.Debug("interface missing; no-op", "dev", r.cfg.Dev)
		return nil
	}

	parsed, err := targets.Parse(r.cfg.HostsRaw)
	if err != nil {
		return err
	}

	desiredHosts := map[string]net.IP{}
	ipSources := map[string]map[string]struct{}{}
	for _, ipnet := range parsed.IPv4Hosts {
		desiredHosts[ipnet.IP.String()] = ipnet.IP
		addSource(ipSources, ipnet.IP.String(), "literal")
	}

	for _, name := range parsed.Names {
		ips, err := r.res.LookupIPv4(ctx, name)
		if err != nil {
			// DNS can be flaky; keep going so we still manage other entries.
			r.log.Warn("dns resolve failed", "name", name, "err", err)
			continue
		}
		for _, ip := range ips {
			desiredHosts[ip.String()] = ip
			addSource(ipSources, ip.String(), name)
		}
	}

	desiredNets := map[string]net.IPNet{}
	for _, n := range parsed.IPv4Nets {
		desiredNets[n.String()] = n
	}

	st, err := r.store.Load()
	if err != nil {
		return err
	}

	ownedHosts := map[string]state.OwnedRoute{}
	ownedNets := map[string]state.OwnedRoute{}
	for _, or := range st.Routes {
		switch or.Kind {
		case state.RouteKindHost:
			ownedHosts[or.Dest] = or
		case state.RouteKindNet:
			ownedNets[or.Dest] = or
		default:
			// ignore unknown kinds
		}
	}

	r.log.Info("reconcile start",
		"dev", r.cfg.Dev,
		"dry_run", r.cfg.DryRun,
		"names_count", len(parsed.Names),
		"literal_hosts_count", len(parsed.IPv4Hosts),
		"cidrs_count", len(parsed.IPv4Nets),
		"desired_hosts_count", len(desiredHosts),
		"desired_nets_count", len(desiredNets),
		"owned_routes_count", len(st.Routes),
	)

	var deletedHosts, deletedNets, ensuredHosts, ensuredNets int

	// Delete stale owned routes first.
	for dest, or := range ownedHosts {
		if _, ok := desiredHosts[dest]; ok {
			continue
		}
		ip := net.ParseIP(dest)
		if ip == nil {
			continue
		}
		if err := r.routes.DeleteHostRoute(ctx, ip, or.Dev); err != nil {
			r.log.Warn("delete host route failed", "dest", dest, "dev", or.Dev, "err", err)
		}
		deletedHosts++
		delete(ownedHosts, dest)
	}
	for dest, or := range ownedNets {
		if _, ok := desiredNets[dest]; ok {
			continue
		}
		_, ipnet, err := net.ParseCIDR(dest)
		if err != nil || ipnet == nil {
			continue
		}
		if err := r.routes.DeleteNetRoute(ctx, *ipnet, or.Dev); err != nil {
			r.log.Warn("delete net route failed", "dest", dest, "dev", or.Dev, "err", err)
		}
		deletedNets++
		delete(ownedNets, dest)
	}

	// Ensure desired routes exist; add to owned set on success.
	for dest, ip := range desiredHosts {
		if err := r.routes.EnsureHostRoute(ctx, r.cfg.Dev, ip); err != nil {
			r.log.Warn("ensure host route failed", "dest", dest, "dev", r.cfg.Dev, "err", err)
			continue
		}
		ensuredHosts++
		ownedHosts[dest] = state.OwnedRoute{
			Kind:    state.RouteKindHost,
			Dest:    dest,
			Dev:     r.cfg.Dev,
			Sources: sourcesSlice(ipSources[dest]),
		}
	}
	for dest, n := range desiredNets {
		if err := r.routes.EnsureNetRoute(ctx, r.cfg.Dev, n); err != nil {
			r.log.Warn("ensure net route failed", "dest", dest, "dev", r.cfg.Dev, "err", err)
			continue
		}
		ensuredNets++
		ownedNets[dest] = state.OwnedRoute{
			Kind:    state.RouteKindNet,
			Dest:    dest,
			Dev:     r.cfg.Dev,
			Sources: []string{"cidr"},
		}
	}

	newState := state.OwnedState{Routes: mergeOwned(ownedHosts, ownedNets)}
	if err := r.store.Save(newState); err != nil {
		return err
	}
	r.log.Info("reconcile done",
		"deleted_hosts", deletedHosts,
		"deleted_nets", deletedNets,
		"ensured_hosts", ensuredHosts,
		"ensured_nets", ensuredNets,
		"owned_routes_count", len(newState.Routes),
	)
	return nil
}

func (r *Reconciler) Cleanup(ctx context.Context) error {
	st, err := r.store.Load()
	if err != nil {
		return err
	}

	var errs []error
	for _, or := range st.Routes {
		switch or.Kind {
		case state.RouteKindHost:
			ip := net.ParseIP(or.Dest)
			if ip == nil {
				continue
			}
			if err := r.routes.DeleteHostRoute(ctx, ip, or.Dev); err != nil {
				errs = append(errs, fmt.Errorf("delete host %s: %w", or.Dest, err))
			}
		case state.RouteKindNet:
			_, ipnet, err := net.ParseCIDR(or.Dest)
			if err != nil || ipnet == nil {
				continue
			}
			if err := r.routes.DeleteNetRoute(ctx, *ipnet, or.Dev); err != nil {
				errs = append(errs, fmt.Errorf("delete net %s: %w", or.Dest, err))
			}
		}
	}

	// Clear state regardless; we want next start to be clean.
	_ = r.store.Save(state.OwnedState{})

	return errorsJoin(errs)
}

func (r *Reconciler) Show(ctx context.Context) error {
	if !netif.Exists(r.cfg.Dev) {
		_, _ = fmt.Fprintf(os.Stdout, "interface %s does not exist\n", r.cfg.Dev)
		return nil
	}

	parsed, err := targets.Parse(r.cfg.HostsRaw)
	if err != nil {
		return err
	}

	desiredHosts := map[string]net.IP{}
	for _, ipnet := range parsed.IPv4Hosts {
		desiredHosts[ipnet.IP.String()] = ipnet.IP
	}
	ipSources := map[string]map[string]struct{}{}
	for _, ipnet := range parsed.IPv4Hosts {
		addSource(ipSources, ipnet.IP.String(), "literal")
	}
	for _, name := range parsed.Names {
		ips, err := r.res.LookupIPv4(ctx, name)
		if err != nil {
			r.log.Warn("dns resolve failed", "name", name, "err", err)
			continue
		}
		for _, ip := range ips {
			desiredHosts[ip.String()] = ip
			addSource(ipSources, ip.String(), name)
		}
	}
	desiredNets := map[string]net.IPNet{}
	for _, n := range parsed.IPv4Nets {
		desiredNets[n.String()] = n
	}

	st, err := r.store.Load()
	if err != nil {
		return err
	}
	managed := map[string]state.OwnedRoute{}
	for _, or := range st.Routes {
		managed[or.Dest] = or
	}

	dests, err := r.routes.ListDestinationsByDev(ctx, r.cfg.Dev)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(os.Stdout, "routes using %s\n", r.cfg.Dev)
	for _, d := range dests {
		tag := "unmanaged"
		if or, ok := managed[d]; ok {
			tag = "managed:" + string(or.Kind)
			if or.Kind == state.RouteKindHost && len(or.Sources) > 0 {
				tag += " (" + strings.Join(or.Sources, ",") + ")"
			}
		} else if src := sourcesSlice(ipSources[d]); len(src) > 0 {
			tag = "desired (" + strings.Join(src, ",") + ")"
		}
		_, _ = fmt.Fprintf(os.Stdout, "- %s [%s]\n", d, tag)
	}

	var missing []string
	for _, ip := range desiredHosts {
		ok, err := r.routes.HostRouteUsesDev(ctx, ip.String(), r.cfg.Dev)
		if err != nil || !ok {
			missing = append(missing, ip.String())
		}
	}
	for _, n := range desiredNets {
		ok, err := r.routes.NetRouteUsesDev(ctx, n.String(), r.cfg.Dev)
		if err != nil || !ok {
			missing = append(missing, n.String())
		}
	}
	sort.Strings(missing)

	if len(missing) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "\nall desired routes are present")
		return nil
	}

	_, _ = fmt.Fprintln(os.Stdout, "\ndesired but missing (or not using dev)")
	for _, m := range missing {
		kind := "host"
		if strings.Contains(m, "/") {
			kind = "net"
		}
		_, _ = fmt.Fprintf(os.Stdout, "- %s [%s]\n", m, kind)
	}
	return nil
}

func mergeOwned(hosts map[string]state.OwnedRoute, nets map[string]state.OwnedRoute) []state.OwnedRoute {
	out := make([]state.OwnedRoute, 0, len(hosts)+len(nets))
	for _, v := range hosts {
		out = append(out, v)
	}
	for _, v := range nets {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Dest < out[j].Dest
	})
	return out
}

func errorsJoin(errs []error) error {
	var out error
	for _, e := range errs {
		if e == nil {
			continue
		}
		if out == nil {
			out = e
			continue
		}
		out = fmt.Errorf("%v; %w", out, e)
	}
	return out
}

func addSource(m map[string]map[string]struct{}, ip string, source string) {
	if ip == "" || source == "" {
		return
	}
	s, ok := m[ip]
	if !ok {
		s = map[string]struct{}{}
		m[ip] = s
	}
	s[source] = struct{}{}
}

func sourcesSlice(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for s := range m {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

