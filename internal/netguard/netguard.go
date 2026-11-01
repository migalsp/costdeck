// Package netguard keeps outbound requests to user-configured endpoints away from the
// cloud metadata services, which hand out the node's cloud credentials to anything that
// asks. Endpoints such as VictoriaMetrics or a self-hosted model server legitimately live
// inside the cluster or on a private network, so only link-local, unspecified and the AWS
// IPv6 and Alibaba Cloud metadata addresses are refused.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// awsIPv6Metadata is the instance metadata service on IPv6-enabled EC2 instances; unlike
// 169.254.169.254 it is not a link-local address.
var awsIPv6Metadata = net.ParseIP("fd00:ec2::254")

var alibabaMetadata = net.ParseIP("100.100.100.200")

// ErrForbiddenAddress is returned for a connection to a metadata address.
var ErrForbiddenAddress = errors.New("connections to link-local and cloud metadata addresses are not allowed")

var ErrLoopbackAddress = errors.New("connections from a cloud service setting to loopback addresses are not allowed")

// Forbidden reports whether ip is a metadata or link-local address.
func Forbidden(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() ||
		ip.Equal(awsIPv6Metadata) || ip.Equal(alibabaMetadata)
}

// ForbiddenHost reports whether a URL host is a literal metadata or link-local address.
// Names are checked when they are dialled.
func ForbiddenHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && Forbidden(ip)
}

func refuseMetadata(ip net.IP) error {
	if Forbidden(ip) {
		return fmt.Errorf("%w: %s", ErrForbiddenAddress, ip)
	}
	return nil
}

func refuseMetadataAndLoopback(ip net.IP) error {
	if err := refuseMetadata(ip); err != nil {
		return err
	}
	if ip.IsLoopback() {
		return fmt.Errorf("%w: %s", ErrLoopbackAddress, ip)
	}
	return nil
}

func controlRefusing(refuse func(net.IP) error) func(string, string, syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: %s", ErrForbiddenAddress, host)
		}
		return refuse(ip)
	}
}

func dialerRefusing(timeout time.Duration, refuse func(net.IP) error) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: controlRefusing(refuse)}
}

// Dialer returns a dialer that refuses metadata addresses, for protocols other than HTTP
// such as SMTP.
func Dialer(timeout time.Duration) *net.Dialer {
	return dialerRefusing(timeout, refuseMetadata)
}

// Transport returns a clone of http.DefaultTransport that refuses metadata addresses, also
// when the request goes through an HTTP proxy.
func Transport() *http.Transport {
	return transportRefusing(refuseMetadata)
}

func ExternalTransport() *http.Transport {
	return transportRefusing(refuseMetadataAndLoopback)
}

func transportRefusing(refuse func(net.IP) error) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = dialerRefusing(30*time.Second, refuse).DialContext
	t.Proxy = guardProxy(t.Proxy, refuse)
	return t
}

func guardProxy(proxy func(*http.Request) (*url.URL, error), refuse func(net.IP) error) func(*http.Request) (*url.URL, error) {
	return func(r *http.Request) (*url.URL, error) {
		u, err := proxy(r)
		if err != nil || u == nil {
			return u, err
		}
		return u, checkProxiedHost(r.Context(), r.URL.Hostname(), refuse)
	}
}

var lookupIPAddr = net.DefaultResolver.LookupIPAddr

func checkProxiedHost(ctx context.Context, host string, refuse func(net.IP) error) error {
	if ip := net.ParseIP(host); ip != nil {
		return refuse(ip)
	}
	addrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if err := refuse(a.IP); err != nil {
			return err
		}
	}
	return nil
}
