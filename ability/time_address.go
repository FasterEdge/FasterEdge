// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"errors"
	"fmt"
	"github.com/FasterEdge/FasterEdge/types"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

var ErrTimeAddressDisallowed = errors.New("time source address is disallowed")

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}
type addressPolicy struct {
	allowPrivate bool
	resolver     ipResolver
	tcpDial      func(context.Context, string, string) (net.Conn, error)
}

func (p addressPolicy) validateIP(ip net.IP) error {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("%w: invalid IP", ErrTimeAddressDisallowed)
	}
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsMulticast() {
		return fmt.Errorf("%w: %s", ErrTimeAddressDisallowed, a)
	}
	if p.allowPrivate {
		return nil
	}
	// IsPrivate excludes CGNAT and several special-use ranges.
	bad := a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
	if a.Is4() {
		v := a.As4()
		n := uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
		for _, r := range [][2]uint32{{0x64400000, 0x647fffff}, {0xc0000000, 0xc00000ff}, {0xc0000200, 0xc00002ff}, {0xc6336400, 0xc63364ff}, {0xcb007100, 0xcb0071ff}, {0x0a000000, 0x0affffff}, {0xac100000, 0xac1fffff}, {0xc0a80000, 0xc0a8ffff}} {
			if n >= r[0] && n <= r[1] {
				bad = true
			}
		}
	} else if a.IsLinkLocalUnicast() {
		bad = true
	}
	if bad {
		return fmt.Errorf("%w: %s", ErrTimeAddressDisallowed, a)
	}
	return nil
}

func (p addressPolicy) resolve(ctx context.Context, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		if err := p.validateIP(ip); err != nil {
			return nil, err
		}
		return []net.IPAddr{{IP: ip}}, nil
	}
	if p.resolver == nil {
		p.resolver = net.DefaultResolver
	}
	ips, err := p.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no addresses", ErrTimeAddressDisallowed)
	}
	for _, x := range ips {
		if err := p.validateIP(x.IP); err != nil {
			return nil, err
		}
	}
	return ips, nil
}

func (p addressPolicy) dialContext(d *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	base := p.tcpDial
	if base == nil {
		base = d.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("%w: %w: %v", types.ErrInvalidArguments, ErrInvalidTimeURL, err)
		}
		ips, err := p.resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		var errs []error
		for _, ip := range ips {
			c, e := base(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if e == nil {
				return c, nil
			}
			errs = append(errs, e)
		}
		return nil, errors.Join(errs...)
	}
}

var ErrInvalidTimeURL = errors.New("invalid time source URL")

func validateTimeURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", types.ErrInvalidArguments, err)
	}
	if u.User != nil || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("%w", types.ErrInvalidArguments)
	}
	if strings.TrimSpace(u.Hostname()) == "" {
		return nil, fmt.Errorf("%w", types.ErrInvalidArguments)
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%w", types.ErrInvalidArguments)
		}
	}
	return u, nil
}
