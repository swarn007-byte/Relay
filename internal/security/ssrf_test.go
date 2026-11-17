package security

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func newTestGuard(allowPrivate bool, ports []int, addresses ...string) *Guard {
	g := NewGuard(allowPrivate, ports)
	g.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range addresses {
			addr, err := netip.ParseAddr(a)
			if err != nil {
				return nil, err
			}
			out = append(out, addr)
		}
		return out, nil
	}
	return g
}

func TestValidateDestinationBlocked(t *testing.T) {
	cases := []struct {
		name      string
		rawURL    string
		addresses []string
	}{
		{"loopback ipv4", "http://127.0.0.1/hook", nil},
		{"loopback with port", "http://127.0.0.1:8080/hook", nil},
		{"private rfc1918", "http://10.1.2.3/hook", nil},
		{"private 192_168", "http://192.168.1.10/hook", nil},
		{"private 172_16", "http://172.16.0.1/hook", nil},
		{"cgnat", "http://100.64.0.1/hook", nil},
		{"cloud metadata", "http://169.254.169.254/latest/meta-data/", nil},
		{"unspecified", "http://0.0.0.0/hook", nil},
		{"multicast", "http://224.0.0.1/hook", nil},
		{"reserved", "http://240.0.0.1/hook", nil},
		{"ipv6 loopback", "http://[::1]/hook", nil},
		{"ipv6 unspecified", "http://[::]/hook", nil},
		{"ipv6 ula", "http://[fc00::1]/hook", nil},
		{"ipv6 link local", "http://[fe80::1]/hook", nil},
		{"ipv6 multicast", "http://[ff02::1]/hook", nil},
		{"ipv4 mapped loopback", "http://[::ffff:127.0.0.1]/hook", nil},
		{"nat64 embedded loopback", "http://[64:ff9b::7f00:1]/hook", nil},
		{"ipv6 documentation", "http://[2001:db8::1]/hook", nil},
		{"ftp scheme", "ftp://example.com/hook", nil},
		{"file scheme", "file:///etc/passwd", nil},
		{"credentials in url", "http://user:pass@example.com/hook", nil},
		{"empty host", "http:///hook", nil},
		{"hostname resolving to metadata", "http://internal.test/hook", []string{"169.254.169.254"}},
		{"hostname resolving to private", "http://intranet.test/hook", []string{"192.168.1.10"}},
		{"hostname mixed public private", "http://mixed.test/hook", []string{"93.184.216.34", "127.0.0.1"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := newTestGuard(false, []int{80, 443}, tc.addresses...)
			if _, err := guard.Validate(context.Background(), tc.rawURL); !errors.Is(err, ErrBlocked) {
				t.Fatalf("expected ErrBlocked for %s, got %v", tc.rawURL, err)
			}
		})
	}
}

func TestValidateDestinationAllowed(t *testing.T) {
	guard := newTestGuard(false, []int{80, 443}, "93.184.216.34")

	allowed := []string{
		"https://example.com/hook",
		"http://example.com",
		"https://example.com:443/hook",
		"https://93.184.216.34/hook",
	}
	for _, raw := range allowed {
		if _, err := guard.Validate(context.Background(), raw); err != nil {
			t.Fatalf("expected %s to be allowed, got %v", raw, err)
		}
	}

	blocked := []string{
		"https://example.com:8443/hook",
		"http://example.com:8080/hook",
		"http://93.184.216.34:22/hook",
	}
	for _, raw := range blocked {
		if _, err := guard.Validate(context.Background(), raw); !errors.Is(err, ErrBlocked) {
			t.Fatalf("expected %s to be blocked, got %v", raw, err)
		}
	}
}

func TestValidateAllowPrivateStillRejectsMalformed(t *testing.T) {
	guard := NewGuard(true, nil)
	ctx := context.Background()

	if _, err := guard.Validate(ctx, "http://127.0.0.1:9999/hook"); err != nil {
		t.Fatalf("private destination must be allowed in dev mode: %v", err)
	}
	if _, err := guard.Validate(ctx, "ftp://example.com/hook"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("scheme must stay restricted: %v", err)
	}
	if _, err := guard.Validate(ctx, "http://user:pass@example.com/hook"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("credentials must stay restricted: %v", err)
	}
}

func TestDialBlocksResolvedPrivateAddress(t *testing.T) {
	guard := newTestGuard(false, []int{80, 443}, "127.0.0.1")
	_, err := guard.DialContext(context.Background(), "tcp", "evil.test:80")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}
}

func TestDialBlocksDisallowedPort(t *testing.T) {
	guard := NewGuard(false, []int{80, 443})
	_, err := guard.DialContext(context.Background(), "tcp", "93.184.216.34:8443")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}
}

func TestDialAllowsPrivateInDevMode(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			conn.Close()
		}
	}()

	guard := NewGuard(true, nil)
	conn, err := guard.DialContext(context.Background(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dev mode dial failed: %v", err)
	}
	conn.Close()
}

func TestDialDefendsAgainstRebinding(t *testing.T) {
	guard := NewGuard(false, []int{80, 443})
	addresses := []netip.Addr{netip.MustParseAddr("93.184.216.34")}
	guard.lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return append([]netip.Addr{}, addresses...), nil
	}

	if _, err := guard.Validate(context.Background(), "http://rebind.test/hook"); err != nil {
		t.Fatalf("initial validation failed: %v", err)
	}

	addresses = []netip.Addr{netip.MustParseAddr("169.254.169.254")}
	_, err := guard.DialContext(context.Background(), "tcp", "rebind.test:80")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("rebound address must be blocked at dial time: %v", err)
	}
}

func TestCheckAddrCoversMetadataRanges(t *testing.T) {
	guard := NewGuard(false, nil)
	blocked := []string{
		"169.254.169.254",
		"169.254.170.2",
		"100.100.100.200",
		"fd00:ec2::254",
		"127.0.0.1",
		"::1",
	}
	for _, raw := range blocked {
		addr := netip.MustParseAddr(raw)
		if err := guard.checkAddr(addr); err == nil {
			t.Fatalf("expected %s to be blocked", raw)
		}
	}
	if err := guard.checkAddr(netip.MustParseAddr("93.184.216.34")); err != nil {
		t.Fatalf("public address blocked: %v", err)
	}
}
