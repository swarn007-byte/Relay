package security

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrBlocked = errors.New("destination address is not allowed")

var blockedV4 = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

var blockedV6 = []netip.Prefix{
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

type lookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)

type Guard struct {
	allowPrivate bool
	allowedPorts map[int]struct{}
	lookup       lookupFunc
	dialer       *net.Dialer
}

func NewGuard(allowPrivate bool, allowedPorts []int) *Guard {
	g := &Guard{
		allowPrivate: allowPrivate,
		allowedPorts: make(map[int]struct{}, len(allowedPorts)),
		dialer:       &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
	for _, p := range allowedPorts {
		g.allowedPorts[p] = struct{}{}
	}
	g.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	return g
}

func (g *Guard) Validate(ctx context.Context, rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme must be http or https", ErrBlocked)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%w: credentials in URL are not allowed", ErrBlocked)
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, fmt.Errorf("%w: missing host", ErrBlocked)
	}
	if portStr := parsed.Port(); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: invalid port", ErrBlocked)
		}
		if err := g.checkPort(port); err != nil {
			return nil, err
		}
	} else if !g.allowPrivate && !g.allowedPortsEmpty() {
		defaultPort := 80
		if parsed.Scheme == "https" {
			defaultPort = 443
		}
		if err := g.checkPort(defaultPort); err != nil {
			return nil, err
		}
	}

	ips, err := g.resolve(ctx, hostname)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if err := g.checkAddr(ip); err != nil {
			return nil, fmt.Errorf("%w: host %q resolves to %s", ErrBlocked, hostname, ip)
		}
	}
	return parsed, nil
}

func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed address %q", ErrBlocked, addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed port in %q", ErrBlocked, addr)
	}
	if err := g.checkPort(port); err != nil {
		return nil, err
	}

	ips, err := g.resolve(ctx, host)
	if err != nil {
		return nil, err
	}

	var blockedErrs []string
	var dialErrs []string
	allowedSeen := false

	for _, ip := range ips {
		if err := g.checkAddr(ip); err != nil {
			blockedErrs = append(blockedErrs, fmt.Sprintf("%s: %v", ip, err))
			continue
		}
		allowedSeen = true
		conn, dialErr := g.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), portStr))
		if dialErr == nil {
			return conn, nil
		}
		dialErrs = append(dialErrs, fmt.Sprintf("%s: %v", ip, dialErr))
	}

	if allowedSeen {
		return nil, fmt.Errorf("dial %s: %s", host, strings.Join(dialErrs, "; "))
	}
	if len(blockedErrs) == 0 {
		return nil, fmt.Errorf("cannot dial host %q", host)
	}
	return nil, fmt.Errorf("%w: host %q (%s)", ErrBlocked, host, strings.Join(blockedErrs, "; "))
}

func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr.Unmap()}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := g.lookup(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve host %q: no addresses", host)
	}
	for i, ip := range ips {
		ips[i] = ip.Unmap()
	}
	return ips, nil
}

func (g *Guard) checkAddr(addr netip.Addr) error {
	if g.allowPrivate {
		return nil
	}
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlocked)
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return fmt.Errorf("%w: address is in a disallowed range", ErrBlocked)
	}
	if addr.Is4() {
		for _, p := range blockedV4 {
			if p.Contains(addr) {
				return fmt.Errorf("%w: address is in a disallowed range", ErrBlocked)
			}
		}
		return nil
	}
	for _, p := range blockedV6 {
		if p.Contains(addr) {
			return fmt.Errorf("%w: address is in a disallowed range", ErrBlocked)
		}
	}
	return nil
}

func (g *Guard) checkPort(port int) error {
	if g.allowPrivate || g.allowedPortsEmpty() {
		return nil
	}
	if _, ok := g.allowedPorts[port]; !ok {
		return fmt.Errorf("%w: port %d is not in the allowed destination ports", ErrBlocked, port)
	}
	return nil
}

func (g *Guard) allowedPortsEmpty() bool {
	return len(g.allowedPorts) == 0
}
