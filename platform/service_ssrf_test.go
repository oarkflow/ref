package platform

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPrivateAddressRanges(t *testing.T) {
	private := []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.1.1", "100.127.255.254", "0.0.0.0", "0.1.2.3", "224.0.0.1", "239.1.1.1", "255.255.255.255",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "ff0e::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:100.64.1.1", "::127.0.0.1",
	}
	for _, s := range private {
		if !privateAddress(netip.MustParseAddr(s)) {
			t.Errorf("%s is not treated as private", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "100.128.0.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if privateAddress(netip.MustParseAddr(s)) {
			t.Errorf("%s is treated as private", s)
		}
	}
}

// The Control hook sees the address actually dialed, after resolution, so a
// name that re-resolves to a private address between checkHost and the dial
// (DNS rebinding) is still refused.
func TestDialControlRefusesPrivateAddresses(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "100.64.1.1:443", "[::ffff:127.0.0.1]:80", "[::1]:80", "169.254.169.254:80"} {
		if err := refusePrivateDial("tcp", addr, nil); err == nil || !strings.Contains(err.Error(), "allow_private_networks") {
			t.Errorf("dial to %s allowed: %v", addr, err)
		}
	}
	if err := refusePrivateDial("tcp", "93.184.216.34:443", nil); err != nil {
		t.Errorf("public dial refused: %v", err)
	}
}

func openTestHTTPService(t *testing.T, host string, allowPrivate bool) *HTTPService {
	t.Helper()
	res, _, err := openHTTPService(context.Background(), ResourceSpec{Name: "svc", Kind: "service.http", Config: map[string]any{
		"allowed_hosts": []any{host}, "allow_private_networks": allowPrivate, "retry_attempts": 1}})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*HTTPService)
}

// The transport itself refuses private destinations, independent of the
// up-front checkHost: a request sent straight through the client (as after a
// rebinding) fails at the dial unless allow_private_networks is set.
func TestTransportRefusesPrivateDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	targets := []string{"http://127.0.0.1:" + port + "/", "http://[::ffff:127.0.0.1]:" + port + "/", "http://100.64.1.1:" + port + "/"}

	blocked := openTestHTTPService(t, "127.0.0.1", false)
	transport := blocked.client.Transport.(*http.Transport)
	transport.Proxy = nil // dial directly, as with no proxy configured
	for _, target := range targets {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		resp, err := transport.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
			t.Errorf("dial to %s succeeded with allow_private_networks=false", target)
		} else if !strings.Contains(err.Error(), "private address") {
			t.Errorf("dial to %s: unexpected error %v", target, err)
		}
	}

	allowed := openTestHTTPService(t, "127.0.0.1", true)
	allowed.client.Transport.(*http.Transport).Proxy = nil
	resp, err := allowed.Do(context.Background(), HTTPRequest{URL: srv.URL})
	if err != nil || string(resp.Body) != "ok" {
		t.Fatalf("allow_private_networks=true: %v %+v", err, resp)
	}
}

// A configured proxy may sit on a private network; the guard exempts the
// proxy's own address.
func TestGuardedTransportKeepsProxyWorking(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("via proxy " + r.URL.Host))
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	svc := openTestHTTPService(t, "api.example.test", false)
	svc.client.Transport.(*http.Transport).Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	guardDialer(svc.client.Transport.(*http.Transport))
	req, _ := http.NewRequest(http.MethodGet, "http://api.example.test/x", nil)
	resp, err := svc.client.Do(req)
	if err != nil {
		t.Fatalf("request through a loopback proxy: %v", err)
	}
	resp.Body.Close()
}
