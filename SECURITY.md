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

- 命令能力（sh 等）走白名单 + 参数校验，拒绝注入
- `OneKeyAbility` 等固定长度结构校验，防溢出/截断
- 网络出站（时间源/SSRF 面）做 IP 校验（禁私有/链路本地/环回/特殊段）+ 重定向降级拦截
- 接收面统一大小上限（1MB 等），防内存耗尽 DoS
- WebSocket/HTTP 监听防慢连接 DoS（读超时/读上限）
- CI 工作流最小权限（permissions 显式声明）

## 致谢

我们感谢所有以负责任方式报告问题的安全研究人员。