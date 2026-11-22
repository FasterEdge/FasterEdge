// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/FasterEdge/FasterEdge/types"
)

// ErrTimeAddressDisallowed 是 TimeAbility 的地址策略哨兵。
// 与全库统一的 ErrAddressDisallowed 是同一个值: 时间源被拒时
// errors.Is 判 ErrTimeAddressDisallowed 或 ErrAddressDisallowed 均成立,
// 不再有第二套判定逻辑可以与之分歧。
var ErrTimeAddressDisallowed = ErrAddressDisallowed

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// addressPolicy 是 TimeAbility 的策略视图, 只做等级映射, 判定全部委托给
// AddressPolicyLevel / AddressGuard(见 address_policy.go)。
// allowPrivate=true 对应 AddressPolicyAllowLoopback: 与旧实现语义逐条对齐
// (回环 + 私网放行, 未指定/组播/链路本地仍拒绝), 因此既有测试无需改动。
type addressPolicy struct {
	allowPrivate bool
	resolver     ipResolver
	tcpDial      func(context.Context, string, string) (net.Conn, error)
}

func (p addressPolicy) level() AddressPolicyLevel {
	if p.allowPrivate {
		return AddressPolicyAllowLoopback
	}
	return AddressPolicyPublicOnly
}

func (p addressPolicy) guard() AddressGuard {
	return AddressGuard{Level: p.level(), Resolver: p.resolver, Dial: p.tcpDial}
}

func (p addressPolicy) validateIP(ip net.IP) error { return p.guard().CheckResolvedIP(ip) }

func (p addressPolicy) resolve(ctx context.Context, host string) ([]net.IPAddr, error) {
	return p.guard().ResolveAndCheck(ctx, host)
}

func (p addressPolicy) dialContext(d *net.Dialer) func(context.Context, string, string) (net.Conn, error) {
	base := p.tcpDial
	if base == nil && d != nil {
		base = d.DialContext
	}
	g := AddressGuard{Level: p.level(), Resolver: p.resolver, Dial: base}
	return g.DialContext
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
