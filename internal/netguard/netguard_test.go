package netguard

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForbidden(t *testing.T) {
	for host, want := range map[string]bool{
		"169.254.169.254": true, "fd00:ec2::254": true, "fe80::1": true, "0.0.0.0": true, "::": true,
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
