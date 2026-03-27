package resolve

import (
	"context"
	"fmt"
	"net"
	"sort"
	"time"
)

type Resolver struct {
	r       *net.Resolver
	timeout time.Duration
}

func New(timeout time.Duration) *Resolver {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Resolver{r: net.DefaultResolver, timeout: timeout}
}

func (res *Resolver) LookupIPv4(ctx context.Context, host string) ([]net.IP, error) {
	cctx, cancel := context.WithTimeout(ctx, res.timeout)
	defer cancel()

	addrs, err := res.r.LookupIPAddr(cctx, host)
	if err != nil {
		return nil, fmt.Errorf("lookup %q: %w", host, err)
	}
	seen := map[string]net.IP{}
	for _, a := range addrs {
		if ip4 := a.IP.To4(); ip4 != nil {
			seen[ip4.String()] = ip4
		}
	}
	out := make([]net.IP, 0, len(seen))
	for _, ip := range seen {
		out = append(out, ip)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

