// Package netguard keeps outbound requests to user-configured endpoints away from the
// cloud metadata services, which hand out the node's cloud credentials to anything that
// asks. Endpoints such as VictoriaMetrics or a self-hosted model server legitimately live
// inside the cluster or on a private network, so only link-local, unspecified and the AWS
// IPv6 metadata addresses are refused.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"
)

// awsIPv6Metadata is the instance metadata service on IPv6-enabled EC2 instances; unlike
// 169.254.169.254 it is not a link-local address.
var awsIPv6Metadata = net.ParseIP("fd00:ec2::254")

// ErrForbiddenAddress is returned for a connection to a metadata address.
var ErrForbiddenAddress = errors.New("connections to link-local and cloud metadata addresses are not allowed")

// Forbidden reports whether ip is a metadata or link-local address.
func Forbidden(ip net.IP) bool {
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.Equal(awsIPv6Metadata)
}

// ForbiddenHost reports whether a URL host is a literal metadata or link-local address.
// Names are checked when they are dialled.
func ForbiddenHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && Forbidden(ip)
}

// control runs after DNS resolution, so a name that resolves to a metadata address, such
// as metadata.google.internal, is refused too.
func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || Forbidden(ip) {
		return fmt.Errorf("%w: %s", ErrForbiddenAddress, host)
	}
	return nil
}

// Transport returns a clone of http.DefaultTransport that refuses metadata addresses.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: control}).DialContext
	return t
}
