<div align="center">
  <img src="https://avatars.githubusercontent.com/u/245985800?s=200&v=4" alt="logo" width="100" />
  <h2>FasterEdge</h2>
  <h3>对称、可靠、安全的多场景边缘计算框架</h3>
</div>

### 一、项目简介

- "我们把每棵树想象成是一个设备上的服务(Atom),这棵树有各式各样的根(Data),这棵树可以从根中吸取养分,并将养分输送至枝干(Ability),枝干上或许有几个鸟巢或蜂窝,这些小家伙会将那'那些东西'从一棵树带到另一棵树,而'那些东西'在自己这棵树上是完全可以被任何部分获取的。"

- 当然这是一颗可大可小的树,甚至支持"水培",以适配各种各样的"生存环境"。您可以开发任何自定义的树枝(Ability)或根系(Data),从而实现一定程度的"极致优化",从而打到不一样的结果。

- 您可以直接使用Go语言版实现大多数平台的打包和部署,或者使用Rust语言版(未推出)、C++语言版(未推出)实现更高性能的边缘计算服务,当然他们之间也是可以相互交互的。

- 这意味着您可以通过这个框架(或者说这种符合"依赖抽象而不依赖具体"的强规范)下的"树"来构建一个相当灵活的分布式的边缘计算系统,甚至是一个分布式的边缘计算生态系统。

<div align="center">
  <img src="./concept.png" style="width:96%;" width="100"/>
</div>

- 图中的小女孩代表着业务或用户(Business/User),这些树枝(Ability)是由树根(Data)提供养分生长出来的。

### 二、术语

- **Atom**: 节点上注册的所有 Data 与 Ability 的容器,提供统一生命周期。
- **Data**: 节点上的"根",承载持久化或运行时状态(配置、密钥、拓扑等)。
- **Ability**: 节点上的"枝干",提供命令式 API 与可选的 Runner / Unmounter 生命周期。
- **Command**: Ability 与 Data 的统一调用接口,`Command(atom, act, args) -> CommandOutput{Name, Value, Err}`。
- **Transport**: 注入到 Ability 内的外部依赖抽象(网络、MQTT、Docker、K8s 等),由用户实现。

### 三、开发模式

```go
// 1. 最小化启动(只含 BaseData + BaseAbility)
atom := fasteredge.InitAtom()

// 2. 或一次性注册常用组件(NetMap/Time/OneKey/Cmd/Sh/Bash/ConfigFile 等)
atom := fasteredge.InitStandardAtom()

// 3. 挂载(Check + Mount)
if err := fasteredge.PreRunAtom(atom); err != nil { return err }

// 4. 在可取消上下文中运行(只对实现 types.Runner 的 Ability 起作用)
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

本地可信代码可直接调用 Ability，例如:

```go
ab, _ := atom.Ability("NetMapAbility")
out := ab.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
    Name: "edge-2", Address: "10.0.0.2:7000", Role: "edge",
})
```

`InitStandardAtom` 会把 `OneKeyAbility` 安装为 Atom 全局命令认证器。HTTP、MQTT、RPC 等远程入口必须使用鉴权分发接口，不应暴露本地可信的 `Command` / `CommandContext`：

```go
credential := ability.OneKeyCredential{
    Subject: token.Subject, IssuedAt: token.IssuedAt,
    ExpiresAt: token.ExpiresAt, Signature: token.Signature,
}
out := atom.AuthenticatedCommandContext(
    ctx, credential, "NetMapAbility", ability.NetMapCommandGetTopology, nil,
)
```

认证会在查找目标组件前完成；过期、吊销、密钥轮换前签发或字段被修改的 token 均会被拒绝。当前 OneKey token 在有效期内可重复使用，因此远程节点应使用较短 TTL，并通过吊销或轮换及时失效。

### 四、生态系统

#### 已实现 Data 组件

| 名称         | 功能                                            | 关键命令                                                          |
|--------------|-------------------------------------------------|-------------------------------------------------------------------|
| `BaseData`   | 框架元信息(logo、版本)                          | `logo` / `info`                                                   |
| `NetMapData` | 本节点网络拓扑(节点名、网卡、默认出网接口)     | `info` / `set_node_name` / `interfaces` / `set_default_iface`     |
| `KeyringData`| 共享密钥与令牌表                                | `status` / `set_secret` / `rotate` / `issue_token` / `revoke_*`  |
| `ConfigData` | 扁平点号路径 KV 配置                            | `get` / `set` / `delete` / `list` / `snapshot`                    |

#### 已实现 Ability 组件

| 名称                       | 类别       | 核心能力                                                                                                                |
|----------------------------|------------|-------------------------------------------------------------------------------------------------------------------------|
| `BaseAbility`              | 基础       | `list_data_names` / `list_ability_names`                                                                                 |
| `RoleAbility`              | 基础       | `describe` / `set_role` / `get_role`                                                                                     |
| `TimeAbility`              | 基础       | `sync_manual` / `sync_system` / `sync_net` / `sync_ntp` / `get_time` / `configure_run`                                  |
| `NetMapAbility`            | 基础       | `register_peer` / `unregister_peer` / `update_peer` / `list_peers` / `lookup_peer` / `get_topology`                     |
| `OneKeyAbility`            | 基础       | `issue_token` / `verify_token` / `revoke_token` / `revoke_all` / `list_tokens` / `status` / `rotate` (HMAC-SHA256)     |
| `CloudRoleAbility`         | 基础       | `describe` / `set_controller` / `register_service` / `set_status` / `heartbeat`(依赖 RoleAbility 且 role=cloud)        |
| `EdgeRoleAbility`          | 基础       | `describe` / `set_zone` / `add_capability` / `record_latency` / `get_metrics` / `set_online`(依赖 RoleAbility 且 role=edge) |
| `CmdAbility`               | 终端       | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `clear_finished`(可插拔 allowlist)                     |
| `ShAbility`                | 终端       | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist`(基于 CmdAbility,sh -c 形式)                              |
| `BashAbility`              | 终端       | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist`(基于 ShAbility,bash --noprofile --norc -c 形式)        |
| `ConfigFileAbility`        | 文件/配置  | `set_path` / `load` / `save` / `exists`(基于 ConfigData 的 JSON 持久化)                                                 |
| `FileTransferAbility`      | 文件/配置  | `set_target` / `upload` / `download` / `list` / `get_transfer` / `cancel`(可插拔 Transport)                            |
| `AlgorithmDistributionAbility` | 文件/配置 | `register_algorithm` / `unregister_algorithm` / `distribute` / `list_distributions` / `cancel`(基于 FileTransfer)   |
| `ModbusAbility`            | 工业协议   | `set_endpoint` / `set_unit_id` / `read_holding` / `read_input` / `read_coils` / `read_discrete` / `write_*`(可插拔)   |
| `SerialAbility`            | 工业协议   | `open` / `close` / `read` / `write` / `set_config` / `list_ports`(可插拔 Transport)                                    |
| `TSNAbility`               | 工业协议   | `set_interface` / `register_talker` / `register_listener` / `unregister` / `set_priority_map` / `set_time_aware`     |
| `MQTTAbility`              | 数据交互   | `set_broker` / `connect` / `disconnect` / `publish` / `subscribe` / `drain` / `list_subscriptions`(可插拔 Transport)  |
| `InfluxDBAbility`          | 数据交互   | `set_endpoint` / `set_token` / `set_org` / `set_bucket` / `ping` / `write` / `query` / `list_series` / `delete_series` |
| `EKuiperAbility`           | 数据交互   | `set_endpoint` / `create_stream` / `drop_stream` / `create_rule` / `start_rule` / `stop_rule` / `show_rules`         |
| `DockerAbility`            | 容器编排   | `set_endpoint` / `list_containers` / `start/stop/restart/remove` / `pull_image` / `inspect` / `get_logs` / `create` |
| `KubernetesAbility`        | 容器编排   | `set_context` / `apply` / `delete` / `list` / `get` / `scale` / `get_logs`                                            |

所有依赖外部网络/进程的能力(FileTransfer / Modbus / Serial / MQTT / InfluxDB / EKuiper / Docker / Kubernetes)均通过 `SetXxxTransport(...)` 注入,框架本身只管理元数据与生命周期,不绑定具体驱动。

#### 跨能力组合示例

```go
atom := fasteredge.InitStandardAtom()
fasteredge.PreRunAtom(atom)

// 1. 通过 NetMap 注册对等节点
nm, _ := atom.Ability("NetMapAbility")
nm.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
    Name: "edge-2", Address: "10.0.0.2:7000", Role: "edge",
})

// 2. 通过 OneKey 为其签发短期令牌
ok, _ := atom.Ability("OneKeyAbility")
out := ok.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
    Subject: "edge-2", TTL: time.Hour,
})
tok := out.Value.(ability.OneKeyToken)

// 3. 远端用 verify_token 校验
ok.Command(atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
    Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature,
})
```

### 五、设计哲学
- 依赖抽象而不依赖具体(Depend on Abstractions, Not on Concrete Implementations.)
- 遵循策略模式(Strategy Pattern)、命令模式(Command Pattern)、组合模式(Composite Pattern)
- 所有命令参数为严格类型,任何 nil/类型不匹配/空白值都会返回 `types.ErrInvalidArguments`
- 内部状态全部受 `sync.RWMutex` / `atomic` 保护,`-race` 干净
- 涉及外部网络的 Ability 默认拒绝 `localhost` / `127.0.0.1` / `0.0.0.0` / `::1`,降低 SSRF 风险

### 六、生命周期与优雅退出

FasterEdge 是库,不是独立进程。先注册组件,再挂载,最后在可取消的上下文中运行:

```go
atom, err := fasteredge.InitAtom()
if err != nil { return err }
if err := fasteredge.PreRunAtom(atom); err != nil { return err }
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

`RunAtom` 会监督实现 `types.Runner` 的 Ability;上下文取消或 Runner 返回错误后,框架使用新的清理上下文按逆序卸载组件。关闭超时、panic 和卸载错误均通过结构化 `error` 返回,组件注册表不会暴露内部 map。

TimeAbility 的命令参数使用严格类型(例如 `TimeSyncManualArgs`),时间缓存基于单调时钟推进。网络时间源默认拒绝本机、私网、链路本地和组播地址,并禁用环境代理;只有显式启用 LAN 源选项时才放行私网地址。HTTP 响应限制为 64 KiB,NTP 使用校验后的响应偏移量。

### 七、测试

```bash
go test ./...           # 运行所有单元测试
go test -race ./...     # 含竞态检测
go test ./... -v -run Integration   # 跨能力集成测试
```

当前覆盖:**~135 个测试,全部通过,`go vet` 无警告。**
