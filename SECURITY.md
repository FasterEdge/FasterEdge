# 安全政策

## 支持的版本

| 版本 | 支持状态 |
| ---- | -------- |
| main (开发分支) | ✅ 支持 |

## 报告安全漏洞

FasterEdge 项目重视安全。请**不要在公共渠道（Issue/讨论区/群聊）公开未修复的漏洞细节**。

请通过以下渠道私密报告：

- **GitHub Security Advisory**（推荐）：在本仓库的 *Security* → *Report a vulnerability* 提交，或直接访问
  `https://github.com/FasterEdge/FasterEdge/security/advisories/new`
- **邮件**：`shun_@outlook.com`（主题请注明 `[FasterEdge Security]`）

### 报告中请包含

1. 漏洞类型与影响组件（Ability / Data / types / 其他）
2. 触发条件与复现步骤（尽量精简）
3. 影响范围（可读性 / 可用性 / 安全性）
4. 建议修复方向（可选）

### 响应承诺

- **48 小时内**：确认收到报告并评估严重性
- **7 天内**：给出修复计划或缓解措施
- **修复发版后**：在致谢中列入报告者（如同意）

## 已知安全加固说明

本项目在多轮暗病审计中已落实以下防护（供安全研究人员参考）：

- 命令执行能力（`CmdAbility` / `ShAbility` / `BashAbility`）为 **fail-closed**：空 allowlist 拒绝一切执行，授权须经 `set_allowlist` 或显式 `set_allowlist_any` opt-in；白名单命中后仍扫描 shell 链接元字符（`;` `&` `|` `$()` 反引号、进程替换、换行/控制字符等）。已知局限：元字符扫描是引号感知的字符级检查而非 shell 解析器，反斜杠转义的编码绕过（如 `\x3b`）不展开解码，须以 fail-closed 缺省或收紧白名单兜底
- `set_allowlist` 与 `set_allowlist_any` 同属能力授予面，均被 `OneKeyAbility` 认证边界限制为仅本地进程内调用（见 `ability/one_key_ability.go` 的 `restrictedRemoteCommand`，由 `TestCmdAbilitySetAllowAnyRemoteRestriction` 直接断言其返回值）；`run` / `start` 可远程调用但始终受 allowlist 约束
- `OneKeyAbility` 等固定长度结构校验，防溢出/截断
- 网络出站（时间源/SSRF 面）做 IP 校验（禁私有/链路本地/环回/特殊段）+ 重定向降级拦截。地址策略统一收敛在 `ability/address_policy.go`：`AddressPolicyLevel` 三档等级（`PublicOnly` / `AllowPrivate` / `AllowLoopback`）由调用方显式选择，net_map/docker/mqtt/influxdb/ekuiper/k8s/time 共用同一处判定真理；域名在拨号期对解析出的**全部**实际 IP 逐个复检后只拨数值 IP（防 DNS rebinding），并拒绝 IPv4-mapped（`::ffff:127.0.0.1`）、userinfo（`tcp://alice@host:port`）与十进制写法（`2130706433` / `127.1` / `0x7f.0.0.1`）等歧义形态
- 接收面统一大小上限（1MB 等），防内存耗尽 DoS
- WebSocket/HTTP 监听防慢连接 DoS（读超时/读上限）
- CI 工作流最小权限（permissions 显式声明）

## 致谢

我们感谢所有以负责任方式报告问题的安全研究人员。