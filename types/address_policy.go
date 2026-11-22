// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package types

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ipResolver 是 AddressGuard 需要的解析器最小面, 便于测试注入 fake
// (不注入时用 net.DefaultResolver)。
type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// AddressPolicyLevel 是出网地址策略等级 —— 由调用方**显式选择**放行范围。
//
// 旧实现里各包各写一套判定(拒绝回环/链路本地/组播、放行私网与否各有取舍),
// 同一地址在不同能力上结论相反, README/SECURITY 无法给出自洽说法。现在判定
// 真理只有一处(CheckAddr / CheckHost), 各能力通过等级表达自己的意图:
//
//   - AddressPolicyPublicOnly: 仅公网可路由地址(默认零值, 最严格);
//   - AddressPolicyAllowPrivate: 额外放行私网/CGNAT/文档保留段(内网 edge、broker、K8s API);
//   - AddressPolicyAllowLoopback: 额外放行回环(仅本机联调/测试服务器)。
//
// 三级**共同**拒绝未指定地址(0.0.0.0/::)、组播、链路本地(169.254.0.0/16、
// fe80::/10、ff02::/16)与受限广播——这些是云元数据 169.254.169.254 与局域网
// 广播的 SSRF 种子。
//
// 放 types 包而不是 ability 包: data/database_data.go 与 ability/* 都要用它,
// 而 ability 已依赖 data(config_file_ability.go 等), 放 ability 会成环。
type AddressPolicyLevel uint8

const (
	AddressPolicyPublicOnly AddressPolicyLevel = iota
	AddressPolicyAllowPrivate
	AddressPolicyAllowLoopback
)

// ErrAddressDisallowed 是所有策略拒绝的统一哨兵(errors.Is 可判定)。
var ErrAddressDisallowed = errors.New("address disallowed by policy")

// specialUseV4Prefixes 是"非公网可路由"的 IPv4 特殊段: 除 netip.IsPrivate()
// 覆盖的 RFC1918 外, 还含 CGNAT(100.64/10)、IETF 协议分配(192.0.0/24)、
// 基准测试(198.18/15)、文档保留(192.0.2/24、198.51.100/24、203.0.113/24)、
// 6to4 中继(192.88.99/24)、保留(240/4)与受限广播(255.255.255.255)。
var specialUseV4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
}

// specialUseV6Prefixes 是 IPv6 侧的文档保留段(ULA fc00::/7 已由 IsPrivate 覆盖)。
var specialUseV6Prefixes = []netip.Prefix{
	netip.MustParsePrefix("2001:db8::/32"),
}

func isSpecialUseAddr(a netip.Addr) bool {
	prefixes := specialUseV6Prefixes
	if a.Is4() {
		prefixes = specialUseV4Prefixes
	}
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// isAlwaysRejectedAddr 判定**任何等级都不放行**的地址: 未指定(0.0.0.0/::)、
// 组播、链路本地(169.254.0.0/16、fe80::/10、ff02::/16)与受限广播
// 255.255.255.255。前几项是云元数据 169.254.169.254 与局域网广播的 SSRF 种子;
// 受限广播本身不是可建连的单播目标, 放行只会把错误推到拨号期。
func isAlwaysRejectedAddr(a netip.Addr) bool {
	if a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() {
		return true
	}
	return a == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// CheckAddr 按等级判定一个已解析的地址。
// IPv4-mapped IPv6(::ffff:127.0.0.1)先 Unmap 再判定 —— 旧实现多处直接与
// 字面量 "::1"/"127.0.0.1" 比较, 该形态可整体绕过。
func (l AddressPolicyLevel) CheckAddr(addr netip.Addr) error {
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrAddressDisallowed)
	}
	// zone(fe80::1%eth0)不参与策略, 剥掉避免 Prefix.Contains 失配。
	a := addr.Unmap().WithZone("")
	if isAlwaysRejectedAddr(a) {
		return fmt.Errorf("%w: %s", ErrAddressDisallowed, a)
	}
	if a.IsLoopback() {
		if l >= AddressPolicyAllowLoopback {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrAddressDisallowed, a)
	}
	if l >= AddressPolicyAllowPrivate {
		return nil
	}
	if a.IsPrivate() || isSpecialUseAddr(a) {
		return fmt.Errorf("%w: %s", ErrAddressDisallowed, a)
	}
	return nil
}

// CheckHost 按等级判定调用方写下的主机(**不做 DNS 解析**, 因此无阻塞风险)。
// 覆盖 IP 字面量(含 IPv4-mapped/方括号形式)与主机名(尾点 FQDN、回环别名、
// 数字型主机名)。
func (l AddressPolicyLevel) CheckHost(host string) error {
	h := strings.TrimSpace(host)
	if h == "" {
		return fmt.Errorf("%w: empty host", ErrAddressDisallowed)
	}
	for _, r := range h {
		if r < ' ' || r == 0x7f || r == ' ' || r == '/' || r == '\\' || r == '?' || r == '#' || r == '@' {
			return fmt.Errorf("%w: malformed host %q", ErrAddressDisallowed, host)
		}
	}
	if strings.HasPrefix(h, "[") || strings.HasSuffix(h, "]") {
		if !strings.HasPrefix(h, "[") || !strings.HasSuffix(h, "]") {
			return fmt.Errorf("%w: malformed host %q", ErrAddressDisallowed, host)
		}
		h = h[1 : len(h)-1]
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return l.CheckAddr(a)
	}
	name := strings.ToLower(strings.TrimSuffix(h, "."))
	if name == "" {
		return fmt.Errorf("%w: empty host %q", ErrAddressDisallowed, host)
	}
	// 十进制/十六进制/八进制写法的 IPv4 字面量(2130706433、127.1、
	// 0x7f.0.0.1、0177.0.0.1)在 Windows 上解析失败、在 glibc 上被当作
	// 127.0.0.1 —— 同一份配置两端结论相反, 一律按不可审计的歧义地址拒绝。
	if isLegacyNumericHost(name) {
		return fmt.Errorf("%w: ambiguous numeric host %q", ErrAddressDisallowed, host)
	}
	if l < AddressPolicyAllowLoopback && (name == "localhost" || strings.HasSuffix(name, ".localhost")) {
		return fmt.Errorf("%w: %s", ErrAddressDisallowed, name)
	}
	return nil
}

func isLegacyNumericHost(name string) bool {
	labels := strings.Split(name, ".")
	if len(labels) > 4 {
		return false
	}
	for _, label := range labels {
		if label == "" {
			return false
		}
		body := label
		if strings.HasPrefix(body, "0x") || strings.HasPrefix(body, "0X") {
			body = body[2:]
			if body == "" || !isHexDigits(body) {
				return false
			}
			continue
		}
		if !isDecimalDigits(body) {
			return false
		}
	}
	return true
}

func isDecimalDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func isHexDigits(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return s != ""
}

// AddressGuard 把策略等级绑定到一次具体的解析 + 拨号:
// 先判定调用方给的主机, 再解析, 对**解析得到的每个实际 IP** 二次判定,
// 最后只用数值 IP 拨号。
//
// 这是防 DNS rebinding 的关键: 名字只解析一次, 校验过的数值 IP 直接交给
// 底层 dialer, 拨号前不会再发生第二次名字解析, 校验与连接之间没有可被
// 替换的窗口(旧实现只在设置期校验主机名, 拨号期重新解析, 属典型 TOCTOU)。
type AddressGuard struct {
	Level AddressPolicyLevel
	// Resolver 为 nil 时用 net.DefaultResolver; 测试注入 fake。
	Resolver ipResolver
	// Dial 为 nil 时用 net.Dialer.DialContext。
	Dial func(context.Context, string, string) (net.Conn, error)
}

// NewAddressGuard 返回指定等级的策略守卫(默认 resolver/dialer)。
func NewAddressGuard(level AddressPolicyLevel) AddressGuard {
	return AddressGuard{Level: level}
}

// CheckHost 按本守卫的等级判定主机(语法层, 不解析 DNS)。
func (g AddressGuard) CheckHost(host string) error { return g.Level.CheckHost(host) }

// CheckResolvedIP 对"到手即校验"的实际 IP 再做一次策略判定。
func (g AddressGuard) CheckResolvedIP(ip net.IP) error {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("%w: invalid IP", ErrAddressDisallowed)
	}
	return g.Level.CheckAddr(a)
}

// ResolveAndCheck 解析主机并按等级校验**全部**解析结果。
// 只要有一个结果不合策略就整体拒绝(混合应答是 DNS rebinding 的常见形态)。
func (g AddressGuard) ResolveAndCheck(ctx context.Context, host string) ([]net.IPAddr, error) {
	if err := g.Level.CheckHost(host); err != nil {
		return nil, err
	}
	h := strings.TrimSpace(host)
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	if a, err := netip.ParseAddr(h); err == nil {
		return []net.IPAddr{{IP: net.IP(a.Unmap().AsSlice())}}, nil
	}
	resolver := g.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, h)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: no addresses for %q", ErrAddressDisallowed, host)
	}
	for _, x := range ips {
		if err := g.CheckResolvedIP(x.IP); err != nil {
			return nil, fmt.Errorf("%w (resolved %s)", err, x.IP)
		}
	}
	return ips, nil
}

// DialContext 满足 net.Dialer 的签名, 可直接塞进 http.Transport.DialContext
// 或 MQTT/Modbus 等注入式 transport 的拨号回调。
func (g AddressGuard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArguments, err)
	}
	ips, err := g.ResolveAndCheck(ctx, host)
	if err != nil {
		return nil, err
	}
	dial := g.Dial
	if dial == nil {
		d := &net.Dialer{}
		dial = d.DialContext
	}
	var errs []error
	for _, ip := range ips {
		// 拨号前最后一次判定: 校验对象就是即将建连的数值 IP。
		if err := g.CheckResolvedIP(ip.IP); err != nil {
			errs = append(errs, err)
			continue
		}
		c, e := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if e == nil {
			return c, nil
		}
		errs = append(errs, e)
	}
	return nil, errors.Join(errs...)
}
