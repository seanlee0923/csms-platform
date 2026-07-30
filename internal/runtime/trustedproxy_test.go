package runtime

import (
	"net/http"
	"testing"
)

func TestTrustedProxyResolverPassthroughWhenUnconfigured(t *testing.T) {
	resolver, err := newTrustedProxyResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"X-Forwarded-For": []string{"203.0.113.7"}}
	if got := resolver.resolve("10.0.0.5:12345", header); got != "10.0.0.5:12345" {
		t.Fatalf("resolve() = %q, want unchanged RemoteAddr", got)
	}
}

func TestTrustedProxyResolverUsesForwardedForWhenRemoteAddrTrusted(t *testing.T) {
	resolver, err := newTrustedProxyResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"X-Forwarded-For": []string{"203.0.113.7, 10.0.0.5"}}
	if got := resolver.resolve("10.0.0.5:12345", header); got != "203.0.113.7" {
		t.Fatalf("resolve() = %q, want leftmost X-Forwarded-For entry", got)
	}
}

func TestTrustedProxyResolverIgnoresForwardedForWhenRemoteAddrNotTrusted(t *testing.T) {
	resolver, err := newTrustedProxyResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"X-Forwarded-For": []string{"203.0.113.7"}}
	if got := resolver.resolve("198.51.100.9:12345", header); got != "198.51.100.9:12345" {
		t.Fatalf("resolve() = %q, want unchanged RemoteAddr for an untrusted peer", got)
	}
}

func TestTrustedProxyResolverIgnoresMalformedForwardedFor(t *testing.T) {
	resolver, err := newTrustedProxyResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{"X-Forwarded-For": []string{"not-an-ip"}}
	if got := resolver.resolve("10.0.0.5:12345", header); got != "10.0.0.5:12345" {
		t.Fatalf("resolve() = %q, want unchanged RemoteAddr for a malformed header", got)
	}
}

func TestNewTrustedProxyResolverRejectsInvalidCIDR(t *testing.T) {
	if _, err := newTrustedProxyResolver([]string{"not-a-cidr"}); err == nil {
		t.Fatal("expected an error for an invalid CIDR")
	}
}
