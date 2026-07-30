package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/seanlee0923/ocpp/csms"
)

// trustedProxyResolver resolves the real client address for a HandshakeAttempt
// arriving through a reverse proxy or Ingress controller. Ingress makes a
// fresh outbound TCP connection to this pod, so csms.HandshakeAttempt.RemoteAddr
// is always the proxy's own address, not the charging station's — without
// this, a per-IP HandshakeLimiter collapses into a single shared budget for
// the entire fleet instead of one per station.
//
// It only trusts X-Forwarded-For when RemoteAddr falls inside one of
// trustedNets: blindly trusting the header would let any client spoof its
// own rate-limit identity by sending an arbitrary X-Forwarded-For value.
// With no trusted CIDRs configured, it is a no-op passthrough — the safe
// default for deployments that don't sit behind a known reverse proxy.
type trustedProxyResolver struct {
	trustedNets []*net.IPNet
}

func newTrustedProxyResolver(cidrs []string) (*trustedProxyResolver, error) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}
	return &trustedProxyResolver{trustedNets: nets}, nil
}

// resolve returns the address a HandshakeLimiter should key its rate limit
// on: the leftmost X-Forwarded-For entry if remoteAddr is a trusted proxy,
// otherwise remoteAddr unchanged. The leftmost entry is only meaningful
// because we assume a single trusted hop directly in front of this process
// (the documented Ingress topology) — with a longer, untrusted proxy chain
// in between, a downstream hop could still forge earlier entries.
func (r *trustedProxyResolver) resolve(remoteAddr string, header http.Header) string {
	if len(r.trustedNets) == 0 {
		return remoteAddr
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !r.isTrusted(ip) {
		return remoteAddr
	}
	forwardedFor := header.Get("X-Forwarded-For")
	if forwardedFor == "" {
		return remoteAddr
	}
	clientAddr := strings.TrimSpace(strings.Split(forwardedFor, ",")[0])
	if net.ParseIP(clientAddr) == nil {
		return remoteAddr
	}
	return clientAddr
}

func (r *trustedProxyResolver) isTrusted(ip net.IP) bool {
	for _, ipNet := range r.trustedNets {
		if ipNet.Contains(ip) {
			return true
		}
	}
	return false
}

// resolvedIPHandshakeLimiter wraps a csms.HandshakeLimiter so it rate-limits
// by the resolved client address instead of the raw connection RemoteAddr.
type resolvedIPHandshakeLimiter struct {
	limiter  csms.HandshakeLimiter
	resolver *trustedProxyResolver
}

func (l *resolvedIPHandshakeLimiter) Allow(ctx context.Context, attempt csms.HandshakeAttempt) bool {
	attempt.RemoteAddr = l.resolver.resolve(attempt.RemoteAddr, attempt.Header)
	return l.limiter.Allow(ctx, attempt)
}
