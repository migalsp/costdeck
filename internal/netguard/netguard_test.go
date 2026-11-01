package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestForbidden(t *testing.T) {
	for host, want := range map[string]bool{
		"169.254.169.254": true, "fd00:ec2::254": true, "fe80::1": true, "0.0.0.0": true, "::": true, "100.100.100.200": true,
		"10.0.0.5": false, "127.0.0.1": false, "192.168.1.10": false, "vmselect.monitoring.svc": false,
	} {
		if got := ForbiddenHost(host); got != want {
			t.Errorf("ForbiddenHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestTransportRefusesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	client := &http.Client{Transport: Transport()}
	if resp, err := client.Get(srv.URL); err != nil {
		t.Fatalf("a private address must stay reachable: %v", err)
	} else {
		_ = resp.Body.Close()
	}
	_, err := client.Get("http://169.254.169.254/latest/meta-data/")
	var op *net.OpError
	if !errors.Is(err, ErrForbiddenAddress) && !errors.As(err, &op) {
		t.Fatalf("the metadata address must be refused, got %v", err)
	}
	if !errors.Is(err, ErrForbiddenAddress) {
		t.Errorf("want ErrForbiddenAddress, got %v", err)
	}
}

func TestExternalTransportRefusesLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	_, err := (&http.Client{Transport: ExternalTransport()}).Get(srv.URL)
	if !errors.Is(err, ErrLoopbackAddress) {
		t.Fatalf("a cloud service setting must not reach loopback, got %v", err)
	}
	_, err = (&http.Client{Transport: ExternalTransport()}).Get("http://169.254.169.254/latest/meta-data/")
	if !errors.Is(err, ErrForbiddenAddress) {
		t.Errorf("want ErrForbiddenAddress for the metadata address, got %v", err)
	}
}

func TestProxiedRequestsAreCheckedBeforeTheProxySeesThem(t *testing.T) {
	proxyURL, _ := url.Parse("http://proxy.corp:3128")
	viaProxy := http.ProxyURL(proxyURL)
	resolved := map[string][]net.IPAddr{
		"metadata.google.internal": {{IP: net.ParseIP("169.254.169.254")}},
		"api.openai.com":           {{IP: net.ParseIP("104.18.7.192")}},
		"sneaky.example.com":       {{IP: net.ParseIP("104.18.7.192")}, {IP: net.ParseIP("127.0.0.1")}},
	}
	saved := lookupIPAddr
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if addrs, ok := resolved[host]; ok {
			return addrs, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { lookupIPAddr = saved })

	tests := []struct {
		name    string
		url     string
		refuse  func(net.IP) error
		wantErr error
	}{
		{"metadata address literal", "http://169.254.169.254/latest/", refuseMetadata, ErrForbiddenAddress},
		{"Alibaba metadata literal", "http://100.100.100.200/latest/", refuseMetadata, ErrForbiddenAddress},
		{"name resolving to metadata", "http://metadata.google.internal/computeMetadata/v1/", refuseMetadata, ErrForbiddenAddress},
		{"public name", "https://api.openai.com/v1", refuseMetadataAndLoopback, nil},
		{"name with a loopback address among others", "https://sneaky.example.com/v1", refuseMetadataAndLoopback, ErrLoopbackAddress},
		{"loopback through the default guard", "http://127.0.0.1:8080/", refuseMetadata, nil},
		{"name only the proxy can resolve", "https://models.internal.corp/v1", refuseMetadataAndLoopback, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.url, nil)
			got, err := guardProxy(viaProxy, tt.refuse)(r)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got != proxyURL {
				t.Errorf("proxy = %v, want %v", got, proxyURL)
			}
		})
	}
}

func TestDirectRequestsAreLeftToTheDialer(t *testing.T) {
	direct := func(*http.Request) (*url.URL, error) { return nil, nil }
	r := httptest.NewRequest(http.MethodGet, "http://169.254.169.254/", nil)
	if u, err := guardProxy(direct, refuseMetadata)(r); u != nil || err != nil {
		t.Errorf("a request without a proxy is checked when dialled; got %v, %v", u, err)
	}
}
