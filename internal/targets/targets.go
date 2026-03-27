package targets

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

type Parsed struct {
	IPv4Hosts []net.IPNet // host IPs as /32
	IPv4Nets  []net.IPNet // CIDRs
	Names     []string
}

func Parse(raw []string) (Parsed, error) {
	var out Parsed

	seenHost := map[string]struct{}{}
	seenNet := map[string]struct{}{}
	seenName := map[string]struct{}{}

	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}

		// CIDR?
		if strings.Contains(item, "/") {
			ip, ipnet, err := net.ParseCIDR(item)
			if err == nil && ip.To4() != nil {
				ipnet.IP = ip.To4()
				key := ipnet.String()
				if _, ok := seenNet[key]; !ok {
					seenNet[key] = struct{}{}
					out.IPv4Nets = append(out.IPv4Nets, *ipnet)
				}
				continue
			}
			return Parsed{}, fmt.Errorf("invalid IPv4 CIDR: %q", item)
		}

		// IPv4 literal?
		ip := net.ParseIP(item)
		if ip != nil && ip.To4() != nil {
			ip4 := ip.To4()
			ipnet := net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}
			key := ipnet.String()
			if _, ok := seenHost[key]; !ok {
				seenHost[key] = struct{}{}
				out.IPv4Hosts = append(out.IPv4Hosts, ipnet)
			}
			continue
		}

		// Otherwise treat as DNS name.
		name := strings.ToLower(item)
		if _, ok := seenName[name]; !ok {
			seenName[name] = struct{}{}
			out.Names = append(out.Names, name)
		}
	}

	sort.Slice(out.IPv4Hosts, func(i, j int) bool { return out.IPv4Hosts[i].IP.String() < out.IPv4Hosts[j].IP.String() })
	sort.Slice(out.IPv4Nets, func(i, j int) bool { return out.IPv4Nets[i].String() < out.IPv4Nets[j].String() })
	sort.Strings(out.Names)
	return out, nil
}

