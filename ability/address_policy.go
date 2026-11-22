// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import "github.com/FasterEdge/FasterEdge/types"

// 地址策略的**判定真理**已下移到 types/address_policy.go —— 原因是
// data/database_data.go 同样要按同一套等级判定(数据库 host / Redis 节点 /
// Influx 端点), 而 ability 已经依赖 data(config_file_ability.go、
// net_map_ability.go、one_key_ability.go、influxdb_ability.go 四个非测试文件),
// 判定若留在 ability, data 侧要么 import cycle, 要么另写一套手写判定
// (旧实现正是后者: database_data.go 的 validateDatabaseHost / validateAddress /
// validateInfluxEndpoint 各写一段 net.ParseIP 比较, 与 failure 语义不一致)。
//
// 这里保持 ability 包原有的公开名字不变(AddressPolicyLevel / 三个等级常量 /
// ErrAddressDisallowed / AddressGuard / NewAddressGuard), 全部是 types 的别名,
// 因此既有调用点与测试无需改动, 也不存在"两套语义"可以分歧。
type AddressPolicyLevel = types.AddressPolicyLevel

const (
	AddressPolicyPublicOnly    = types.AddressPolicyPublicOnly
	AddressPolicyAllowPrivate  = types.AddressPolicyAllowPrivate
	AddressPolicyAllowLoopback = types.AddressPolicyAllowLoopback
)

// ErrAddressDisallowed 是所有策略拒绝的统一哨兵(errors.Is 可判定)。
var ErrAddressDisallowed = types.ErrAddressDisallowed

// AddressGuard 把策略等级绑定到一次具体的解析 + 拨号, 实现见
// types/address_policy.go(防 DNS rebinding: 名字只解析一次, 校验过的数值 IP
// 直接交给底层 dialer)。
type AddressGuard = types.AddressGuard

// NewAddressGuard 返回指定等级的策略守卫(默认 resolver/dialer)。
func NewAddressGuard(level AddressPolicyLevel) AddressGuard {
	return types.NewAddressGuard(level)
}
