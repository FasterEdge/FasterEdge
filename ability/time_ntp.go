package ability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/beevik/ntp"
)

// ntpQuerier is the narrow seam used by TimeAbility so tests never need the network.
type ntpQuerier interface {
	QueryWithOptions(string, ntp.QueryOptions) (*ntp.Response, error)
}

type ntpQueryAdapter struct{}

func (ntpQueryAdapter) QueryWithOptions(address string, options ntp.QueryOptions) (*ntp.Response, error) {
	return ntp.QueryWithOptions(address, options)
}

func normalizeNTPAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", fmt.Errorf("empty NTP address")
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address, nil
	}
	// A bare IPv6 literal needs brackets before adding the NTP port.
	if strings.Count(address, ":") > 1 {
		if strings.HasPrefix(address, "[") {
			return "", fmt.Errorf("invalid NTP address %q", address)
		}
		return net.JoinHostPort(address, "123"), nil
	}
	return net.JoinHostPort(address, "123"), nil
}

func (p addressPolicy) dialUDP(timeout time.Duration) func(string, string) (net.Conn, error) {
	return func(localAddress, remoteAddress string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(remoteAddress)
		if err != nil {
			return nil, err
		}
		ctx := context.Background()
		resolver := p
		ips, err := resolver.resolve(ctx, host)
		if err != nil {
			return nil, err
		}
		d := net.Dialer{Timeout: timeout}
		if localAddress != "" {
			// LocalAddress is advisory in ntp; avoid parsing failures and let the
			// standard dialer select the interface.
			_ = localAddress
		}
		var errs []error
		for _, ip := range ips {
			addr := net.JoinHostPort(ip.IP.String(), port)
			conn, e := d.DialContext(ctx, "udp", addr)
			if e == nil {
				_ = conn.SetDeadline(time.Now().Add(timeout))
				return conn, nil
			}
			errs = append(errs, e)
		}
		return nil, errors.Join(errs...)
	}
}

func (t *TimeAbility) fetchNTPTime(address string) (time.Time, error) {
	t.ensureDefaults()
	server, err := normalizeNTPAddress(address)
	if err != nil {
		return time.Time{}, err
	}
	// Resolve and validate every result before handing control to the client;
	// the policy-aware dialer then connects only to numeric addresses.
	if _, err := t.networkPolicy.resolve(context.Background(), ntpHost(server)); err != nil {
		return time.Time{}, err
	}
	t.mu.RLock()
	q, timeout, clock, policy := t.ntpQuery, t.ntpTimeout, t.clock, t.networkPolicy
	t.mu.RUnlock()
	if q == nil {
		return time.Time{}, errors.New("NTP query client is unavailable")
	}
	resp, err := q.QueryWithOptions(server, ntp.QueryOptions{
		Timeout:       timeout,
		Dialer:        policy.dialUDP(timeout),
		GetSystemTime: clock.Now,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("NTP query %s: %w", server, err)
	}
	if resp == nil {
		return time.Time{}, errors.New("NTP query returned nil response")
	}
	if err := resp.Validate(); err != nil {
		return time.Time{}, err
	}
	return clock.Now().Add(resp.ClockOffset), nil
}

func ntpHost(server string) string {
	host, _, err := net.SplitHostPort(server)
	if err != nil {
		return server
	}
	return strings.Trim(host, "[]")
}
