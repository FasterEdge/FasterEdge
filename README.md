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

// 2. 或一次性注册常用组件(身份/角色/网络/时间/加密/终端/文件配置 + 6 个 DB Data)
atom := fasteredge.InitStandardAtom()

// 3. 挂载(Check + Mount)
if err := fasteredge.PreRunAtom(atom); err != nil { return err }

// 4. 在可取消上下文中运行(只对实现 types.Runner 的 Ability 起作用)
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

`InitStandardAtom` 实际注册的组件(`init.go`):Data 侧 `NetMapData` / `KeyringData` / `ConfigData` / `MySQLData` / `PostgreSQLData` / `SQLiteData` / `RedisData` / `MongoDBData` / `InfluxDBData`;Ability 侧 `RoleAbility` / `TimeAbility` / `NetMapAbility` / `OneKeyAbility` / `CmdAbility` / `ShAbility` / `BashAbility` / `ConfigFileAbility`,外加 `InitAtom` 的 `BaseData` + `BaseAbility`。Docker / K8s / MQTT 等能力需自行追加注册;六个 DB Data 已含于骨架,请勿重复注册。

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

认证与授权都在查找目标组件前完成；过期、吊销、密钥轮换前签发或字段被修改的 token 均会被拒绝，作用域外的 `component`/`command` 同样被拒(返回 `types.ErrAuthorizationDenied`，与凭据错误的 `types.ErrAuthenticationFailed` 可区分)。

令牌可绑定**能力作用域**(component + command 白名单，可选 args 指纹)，签发时声明：

```go
out := ok.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
    Subject: "edge-2", TTL: time.Hour,
    Scope: []data.KeyringScopeRule{
        {Component: "NetMapAbility", Commands: []string{ability.NetMapCommandGetTopology}},
        {Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
    },
})
```

作用域与 nonce 都进入 HMAC 签名载荷，因此远端篡改凭据里的 `Scope` 字段(扩展白名单、剥离字段、改命令表)会使验签失败。空作用域的令牌不限制调用范围——这是旧格式令牌的语义；新签发应显式声明作用域。

一次性令牌(防重放)用 `OneShot: true` 签发：每次鉴权消耗一个 nonce，重放即被拒。未声明 `OneShot` 的令牌在有效期内可重复使用(与历史行为一致)，此时应使用较短 TTL 并通过吊销/轮换及时失效。认证失败按 subject 限速(默认每分钟 10 次)，超限后该 subject 的尝试验证被拒绝。

边界说明：`verify_token` 是认证协议自身依赖的命令，恒不受作用域限制；`issue_token`/`revoke_*`/`rotate`/`set_secret`/`set_allowlist`/`set_role` 等管理命令仅限本地进程内调用，与令牌作用域无关。

#### 可观测性

各生命周期相位(`check` / `mount` / `run` / `unmount` / `rollback`)与命令分派(`command`)都会发出埋点事件。装配只需一行：

```go
// 只接指标
metrics, err := fasteredge.WithMetrics(atom)
if err != nil { return err }

// 或指标 + 自定义 sink(日志、告警等)
metrics, err := fasteredge.WithEventSinkAndMetrics(mySink, atom)
if err != nil { return err }

defer func() {
    // Prometheus 文本格式(text/plain; version=0.0.4), 或 metrics.WriteJSON()
    fmt.Print(metrics.WritePrometheus())
}()
```

各相位成对发事件(`start` + `success`/`failure`),卸载与回滚超时也会补一条终态事件,因此卡死的组件不会留下裸 `start`。导出通道由使用者自选——框架不起 HTTP 服务、不写文件,只提供渲染器。`types.MetricsCollector` 实现 `types.EventSink`,可与 `types.NewMultiEventSink` 组合;只要原始事件、不要聚合时用 `types.EventRecorder`。

### 四、生态

#### 已实现 Data 组件

| 名称                           | 功能                                       | 关键命令                                                                           |
|--------------------------------|--------------------------------------------|------------------------------------------------------------------------------------|
| `BaseData`                     | 框架元信息(logo、版本)                     | `logo` / `info`                                                                    |
| `NetMapData`                   | 本节点网络拓扑(节点名、网卡、默认出网接口) | `info` / `set_node_name` / `interfaces` / `set_default_iface`                      |
| `KeyringData`                  | 共享密钥与令牌表                           | `status` / `set_secret` / `rotate` / `issue_token` / `list_tokens` / `revoke_token` / `revoke_all`                              |
| `ConfigData`                   | 扁平点号路径 KV 配置                       | `get` / `set` / `delete` / `list` / `snapshot`                                     |
| `MySQLData` / `PostgreSQLData` | 关系数据库公开连接参数与独立密码           | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `SQLiteData`                   | SQLite 文件、模式、WAL、超时与连接池参数   | `configure` / `get_config` / `status` / `snapshot`                                 |
| `RedisData` | Redis 节点列表、DB、TLS、超时、连接池与独立密码 | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `MongoDBData` | MongoDB 节点、认证源、副本集、TLS 与独立密码 | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `InfluxDBData` | InfluxDB Endpoint、Org、Bucket、超时与独立 Token | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |

数据库密码和 Token 仅保存在 Data 的私有内存中，`get_config`、`snapshot` 和 `JSONMarshal` 均不会返回明文。配置或密钥每次变化都会递增 `DatabaseStatus.Revision`。数据库端点（含 `InfluxDBData`）统一走第 5 节描述的同一套地址策略：SQL / Redis / MongoDB 的主机取 `AllowPrivate` 等级（边缘拓扑里私网本就合法），InfluxDB 端点取 `PublicOnly`。例如：

```go
mysql, _ := atom.Data("MySQLData")
mysql.Command(atom, data.DatabaseCommandConfigure, data.SQLDatabaseConfigureArgs{
    Config: data.SQLDatabaseConfig{
        Host: "db.example.com", Port: 3306, Database: "edge",
        Username: "edge-node", TLSMode: data.DatabaseTLSRequire,
    },
})
mysql.Command(atom, data.DatabaseCommandSetSecret, data.DatabaseSetSecretArgs{
    Secret: []byte(os.Getenv("MYSQL_PASSWORD")),
})
```

`InfluxDBAbility` 依赖 `InfluxDBData`：旧的 `set_endpoint` / `set_org` / `set_bucket` / `set_token` 命令仍兼容，但配置和 Token 实际存放在 Data 中；`get_config` 返回的 `Token` 始终为空。Ping、写入和查询通过注入的 `InfluxTransport` 完成。

#### 已实现 Ability 组件

下表命令名均为 `go run ./cmd/contract -repo . -stdout` 提取出的字面量,每一行都与源码里的分派分支 1:1 对应。

| 名称                           | 类别      | 核心能力(命令名与 `cmd/contract` 提取的字面量逐条一致)                                                                                                                                                                    |
|--------------------------------|-----------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `BaseAbility`                  | 基础      | `list_data_names` / `list_ability_names`                                                                                                                                   |
| `RoleAbility`                  | 基础      | `describe` / `set_role` / `get_role`                                                                                                                                       |
| `TimeAbility`                  | 基础      | `sync_manual` / `sync_system` / `sync_net` / `sync_ntp` / `get_time` / `configure_run` / `last`                                                                             |
| `NetMapAbility`                | 基础      | `register_peer` / `unregister_peer` / `update_peer` / `list_peers` / `lookup_peer` / `get_topology`                                                                        |
| `OneKeyAbility`                | 基础      | `issue_token` / `verify_token` / `revoke_token` / `revoke_all` / `list_tokens` / `status` / `rotate` (HMAC-SHA256；令牌可绑定 component/command 作用域与一次性 nonce) |
| `CloudRoleAbility`             | 基础      | `describe` / `set_controller` / `get_controller` / `register_service` / `unregister_service` / `list_services` / `set_status` / `get_status` / `heartbeat`(依赖 RoleAbility 且 role=cloud)                        |
| `EdgeRoleAbility`              | 基础      | `describe` / `set_zone` / `get_zone` / `set_capabilities` / `add_capability` / `remove_capability` / `list_capabilities` / `record_latency` / `get_metrics` / `set_online`(依赖 RoleAbility 且 role=edge)        |
| `CmdAbility`                   | 终端      | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `get_allowlist` / `set_allowlist_any` / `get_job` / `clear_finished`(可插拔 allowlist,**fail-closed**)                      |
| `ShAbility`                    | 终端      | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `get_allowlist` / `set_allowlist_any`(基于 CmdAbility,sh -c 形式,不继承 get_job/clear_finished,**fail-closed**)          |
| `BashAbility`                  | 终端      | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `get_allowlist` / `set_allowlist_any`(基于 ShAbility,bash --noprofile --norc -c 形式,不继承 get_job/clear_finished,**fail-closed**) |
| `ConfigFileAbility`            | 文件/配置 | `set_path` / `get_path` / `load` / `save` / `exists`(基于 ConfigData 的 JSON 持久化)                                                                                       |
| `FileTransferAbility`          | 文件/配置 | `set_target` / `get_target` / `upload` / `download` / `list` / `get_transfer` / `cancel` / `clear_finished`(可插拔 Transport)                                              |
| `AlgorithmDistributionAbility` | 文件/配置 | `register_algorithm` / `unregister_algorithm` / `get_algorithm` / `list_algorithms` / `distribute` / `cancel` / `list_distributions` / `clear_finished`(基于 FileTransfer) |
| `ModbusAbility`                | 工业协议  | `set_endpoint` / `get_endpoint` / `set_unit_id` / `get_unit_id` / `read_holding` / `read_input` / `read_coils` / `read_discrete` / `write_coil` / `write_holding` / `write_holding_multi`(可插拔)                |
| `SerialAbility`                | 工业协议  | `open` / `close` / `is_open` / `read` / `write` / `set_config` / `get_config` / `list_ports`(可插拔 Transport)                                                              |
| `TSNAbility`                   | 工业协议  | `set_interface` / `get_interface` / `register_talker` / `register_listener` / `unregister` / `list_streams` / `set_priority_map` / `get_priority_map` / `set_time_aware` / `get_time_aware`(可插拔 `TSNTransport`)                                |
| `MQTTAbility`                  | 数据交互  | `set_broker` / `get_broker` / `set_client_id` / `set_credentials` / `connect` / `disconnect` / `is_connected` / `publish` / `subscribe` / `unsubscribe` / `drain` / `list_subscriptions`(可插拔 Transport)        |
| `InfluxDBAbility`              | 数据交互  | `set_endpoint` / `get_endpoint` / `set_token` / `set_org` / `set_bucket` / `get_config` / `ping` / `write` / `query` / `list_series` / `delete_series`(配置与 Token 实际存放在 `InfluxDBData`)                    |
| `EKuiperAbility`               | 数据交互  | `set_endpoint` / `get_endpoint` / `create_stream` / `drop_stream` / `get_stream` / `list_streams` / `create_rule` / `start_rule` / `stop_rule` / `get_rule_status` / `drop_rule` / `show_rules`                     |
| `DockerAbility`                | 容器编排  | `set_endpoint` / `get_endpoint` / `list_containers` / `start_container` / `stop_container` / `restart_container` / `remove_container` / `create_container` / `inspect_container` / `pull_image` / `get_logs` |
| `KubernetesAbility`            | 容器编排  | `set_context` / `get_context` / `apply` / `delete` / `list` / `get` / `scale` / `get_logs`                                                                                 |

所有依赖外部网络/进程的能力(FileTransfer / AlgorithmDistribution / Modbus / Serial / TSN / MQTT / InfluxDB / EKuiper / Docker / Kubernetes)均通过各自的 `SetTransport(...)` 注入(`serial_ability.go:101`、`tsn_ability.go:164`、`mqtt_ability.go:216`、`modbus_ability.go:108`、`docker_ability.go:104`、`kubernetes_ability.go:145`、`ekuiper_ability.go:116`、`influxdb_ability.go:96`、`file_transfer_ability.go:183`,后者由 `algorithm_distribution_ability.go:174` 透传),框架本身只管理元数据与生命周期,不绑定具体驱动。

#### SerialAbility 的真实后端与适用范围

`SerialAbility` 自身不打开任何设备文件:字节流必须经 `SetTransport(...)` 注入的 `SerialTransport` 完成,框架只管理端口开关状态与配置记录。**未注入 transport 时 `open` 直接返回错误(`no transport set`),不会静默成功**;`is_open` / `get_config` / `list_ports` 只回读本地记录,可离线调用。

`examples/verify/serial_transport.go` 提供一份**最小真实**参考实现(仅用于验证程序,不入主库依赖)。该文件自身没有 build tag,真正按 GOOS 互斥的是同目录下的 `serial_port_windows.go`(`//go:build windows`)/ `serial_port_linux.go`(`//go:build linux`)/ `serial_port_unsupported.go`(`//go:build !windows && !linux`)三份平台后端:

- **Windows**:`CreateFile("\.\COMx")` + `SetCommState`(DCB 波特率/数据位/停止位/校验)+ `SetCommTimeouts`(读超时落点),端口枚举走注册表 `HARDWARE\DEVICEMAP\SERIALCOMM`;
- **Linux**:`open("/dev/ttyUSB*")` + termios `TCGETS`/`TCSETS` + `select` 读超时,端口枚举扫描 `/dev` 下 `ttyS*`/`ttyUSB*`/`ttyACM*`;
- **其他平台**(darwin/wasm 等):`serial_port_unsupported.go` 显式返回"未实现"错误,使 `go build ./...` 在任何 GOOS 下都通过,而不会假装成功。本次改动已在 `darwin/arm64`、`linux/arm64`、`windows/arm64`、`js/wasm` 四种 GOOS 下实测构建,exit code 均为 0。

`examples/verify` 把它注入后跑真实往返:`open → is_open → write → get_config → close`。本次改动在构建机上(`examples/verify` 内 `go run .`)对枚举出的 `COM1` 实测通过:`real-write` 报告写入 9 字节,`real-get_config` 报告 `115200-8N1`。该段毕竟需要物理串口设备,且取的是枚举出的 `ports[0]`(而非硬编码端口名),因此**无法仅凭仓库离线复现**;无可用设备时该段明确 SKIP 而不是伪装通过(`verify_skeleton.go` 的 `[SKIP] Serial/real-roundtrip` 分支)。`ability/serial_ability_test.go` 与 `examples/verify/serial_transport_test.go` 分别覆盖状态机与真实后端的错误路径——这两个测试不需要硬件,属于 `go test ./...` 的一部分。

`TSNAbility` 自身不调用 netlink/tc/taprio:接口探测、流规则下发/撤销、PCP→队列映射、Qbv 门控全部委托给注入的 `TSNTransport`。**未注入 transport 时,`set_interface` / `register_talker` / `register_listener` / `unregister` / `set_priority_map` / `set_time_aware` 一律返回错误,不会静默成功**;`get_interface` / `list_streams` / `get_priority_map` / `get_time_aware` 只回读本地记录,可离线调用。设备侧下发失败时本地状态不前进(例如 `unregister` 撤销失败会保留条目以便重试)。

#### 命令执行能力的安全语义(fail-closed)

`CmdAbility` / `ShAbility` / `BashAbility` 的授权模型是 **fail-closed**,默认拒绝执行而不是默认放行:

- **空 allowlist = 拒绝一切执行**。`run` / `start` 在未配置白名单时返回 `types.ErrInvalidArguments` 包裹的 `ErrAllowlistEmpty`,错误信息同时给出 opt-in 指引。旧版实现把"空 allowlist"解释为"不限制",等价于默认全放行,已修复。
- **显式授权两条路径**:
  1. `set_allowlist` 列出允许执行的命令(CmdAbility 按可执行文件名 + 参数前缀匹配;Sh/Bash 按命令首词匹配);
  2. `set_allowlist_any`(`CmdSetAllowAnyArgs{Any:true}`,或 Go API `SetAllowAny(true)`)主动打开"允许任意命令"——这是**使用者自己的决定**,框架不会替你做这个决定。
- **远程调用面(含一处已登记的缺口)**:`set_allowlist` 是能力授予面,`OneKeyAbility` 的认证边界已把它限制为仅本地进程内调用;`run` / `start` 可远程调用但始终受 allowlist 约束。**新增的 `set_allowlist_any` 尚未加入该本地限制清单**——它需要在 `ability/one_key_ability.go` 的 `restrictedRemoteCommand` 里补一条判断,而该文件在本轮改动期间由其他负责人并行重构,本轮按约定未触碰,缺口已在 `TestCmdAbilitySetAllowAnyRemoteRestriction` 中登记。在该行补齐之前,不要把 `set_allowlist_any` 暴露给远程传输层。
- **复合命令拦截(诚实表述,非完备)**:白名单命中首词后仍会扫描 shell 链接元字符(`;` `&` `|` `<` `>` `$()` 反引号 `` ` `` 进程替换 `<(` `>()` `|&` `${` 以及换行/回车等控制字符),命中即整体拒绝。该扫描是**引号感知的字符级检查,不是 shell 解析器**:反斜杠转义的编码绕过(如 `\x3b` 分号)不展开解码,由调用方通过收紧白名单或坚持 fail-closed 兜底。相关边界用例见 `ability/sh_ability_test.go` 的 `TestShAbilityRejectsExtendedChainingBypass`。

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
- 涉及外部网络的能力默认拒绝 `localhost` / `127.0.0.1` / `0.0.0.0` / `::1`,降低 SSRF 风险。地址判定统一收敛在 `ability/address_policy.go`——它是别名层,实现位于 `types/address_policy.go`(放 `types` 是为了让 `data/` 复用同一套规则而不产生 import cycle),三档等级(`PublicOnly` / `AllowPrivate` / `AllowLoopback`),各能力显式选择等级而不是各写一套判定;IPv4-mapped IPv6(`::ffff:127.0.0.1`)、`tcp://alice@host:port` userinfo 手法与 `2130706433` / `127.1` / `0x7f.0.0.1` 十进制写法一律拒绝,尾点 FQDN(`host.example.com.`)按域名放行
- **拨号期复核的实际覆盖面(如实说明)**:目前只有 `TimeAbility` 在拨号期对解析出的实际 IP 做二次校验(防 DNS rebinding)——`ability/time_http.go:28` 把 `AddressGuard` 装进 `http.Transport.DialContext`,`ability/time_ntp.go:57` 先对解析出的每个 IP 复检、再由 `:74` 拨数值 IP,`ability/time_address.go:59` 是共用的 `AddressGuard.DialContext`。其余能力(Docker / MQTT / K8s / InfluxDB / EKuiper,以及各 DB Data 端点)只在**存储期**做语法/字面量层 `CheckHost` 校验(`docker_ability.go:374`、`mqtt_ability.go:622`、`kubernetes_ability.go:475`/`:497`、`influxdb_ability.go:32`、`ekuiper_ability.go:186`,以及 `data/database_data.go` 的 `validateDatabaseHost`(`:130`)/`validateAddress`(`:153`)/`validateInfluxEndpoint`(`:362`),三者均已委托给同一策略),**拨号期复核由注入的 transport 实现负责**——框架本身不替它们拨号,因此不能声称全部能力都有 DNS rebinding 防护
- **`localhost` 是按能力显式开启的。** `ModbusAbility.set_endpoint` 接受 RTU 设备路径(`/dev/ttyUSB0:9600:8N1`)与 TCP 端点;TCP 端点默认取 `AllowPrivate` 等级(工业从站通常就在 10.x/172.16-31.x/192.168.x),`127.0.0.1:502` 需调用方显式 `SetAllowLoopback(true)` 才放行——与 TimeAbility 的 LAN 源选项是同一种纪律。`FileTransferAbility` 只守卫**目标对端地址**(取自 `NetMapAbility`,`AllowPrivate`);`LocalPath` / `RemotePath` 是文件路径,刻意不走地址策略

### 六、生命周期与优雅退出

FasterEdge 是库,不是独立进程。先注册组件,再挂载,最后在可取消的上下文中运行:

```go
atom := fasteredge.InitAtom()   // InitAtom 单返回值,无 error
if err := fasteredge.PreRunAtom(atom); err != nil { return err }
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

`RunAtom` 会监督实现 `types.Runner` 的 Ability;上下文取消或 Runner 返回错误后,框架使用新的清理上下文按逆序卸载组件。关闭超时、panic 和卸载错误均通过结构化 `error` 返回,组件注册表不会暴露内部 map。运行期的三个只读查询接口:`atom.CommandNames()`(各组件已声明的命令目录)、`atom.Status()`(生命周期快照)与 `atom.Health(ctx)`(逐组件健康聚合,单个检查有 2 秒超时);`atom.Reset()` 可把 `Stopped`/`Failed` 状态的 Atom 复位回 `Created` 以便重新挂载(已注册组件保留,仅清理挂载簿记)。

TimeAbility 的命令参数使用严格类型(例如 `TimeSyncManualArgs`),时间缓存基于单调时钟推进。网络时间源默认拒绝本机、私网、链路本地和组播地址,并禁用环境代理;只有显式启用 LAN 源选项时才放行私网地址。HTTP 响应限制为 64 KiB,NTP 使用校验后的响应偏移量。

### 七、测试

```bash
go test ./...           # 运行所有单元测试
go test -race ./...     # 含竞态检测
go test ./... -v -run 'Integration|Combination|Batch6|Smoke'   # 跨能力集成/组合测试
```

`-run Integration` 只匹配 3 个函数(其中 1 个 SKIP),`TestSmokeEveryAbility`、`TestCombination*`(11 个)、`TestBatch6*`(9 个)都匹配不到,因此集成面要用上面的正则。该正则实测跑出 24 个顶层用例(22 PASS / 2 SKIP)——两条 SKIP 分别是 `TestIntegrationShBashEcho` 与 `TestCombinationTimeSh`,在 Windows 上自行跳过(缺 POSIX shell),在 Linux/macOS 上会真正执行。覆盖全骨架生命周期、DB Data 组合与原子性、Influx Data↔Ability 联动、令牌认证链、NetMap/ConfigFile/FileTransfer/Time+Sh 跨能力组合。

除单元与集成测试外,`ability/fuzz_test.go` 有 8 个 Fuzz 目标(`FuzzShellQuote` / `FuzzIsValidMAC` / `FuzzIsValidSerialPort` / `FuzzIsValidModbusEndpoint` / `FuzzDecodeFromTransmission` / `FuzzIsAcceptableBrokerURL` / `FuzzIsAcceptableControllerURL` / `FuzzModbusEncodingRoundTrip`),`ability/bench_test.go` 有 8 个 Benchmark(`BenchmarkRoleAbilitySetGet` / `BenchmarkKeyringDataIssueAndVerify` / `BenchmarkNetMapAbilityRegisterLookup` / `BenchmarkConfigDataGetSet` / `BenchmarkModbusReadHolding` / `BenchmarkMQTTPublishDrain` / `BenchmarkFullAtomInitStandardAtom` / `BenchmarkAtomMountAndUnmount`)。Benchmark 用 `go test -run '^$' -bench=. -benchmem ./ability` 运行(只跑 Benchmark,跳过 Test),Fuzz 目标用 `go test -fuzz=FuzzXxx -fuzztime=Ns ./ability` 跑指定时长。

当前覆盖:**全部单元与集成测试通过,`go vet` 无警告,`-race` 干净**(本次实测:6 个包全部 `ok`)。测试函数总数 415 个(`go test ./... -list 'Test.*' | grep -c '^Test'`);该数字随组件增长而变化,引用时请以当次实测为准。

### 八、命令契约提取与生态比对

生态里存在第二实现(如 [Simulator](https://github.com/FasterEdge/Simulator) 的前端 registry),它们靠人工与 Go 侧对齐命令名。为把"对齐"变成机器可核验的事实,本仓库提供静态契约提取器:

```bash
go run ./cmd/contract -out contract/contract.json   # 也可 -stdout 直接打印
```

提取器用 `go/ast` 解析 `ability/` 与 `data/` 下每个组件的 `Command()`:取 `switch act` 的每条分派分支(含经 helper 委托的链路,如 `simpleContainerAction(act, args)` / `sqlCommand(store, act, args)` / `b.sh.Command(mapBashCommand(act), args)`),把 case 标签的常量标识符回查同文件 const 得出命令字面量,再解析该分支里 `args.(XxxArgs)` 的参数结构体字段(名字 / 类型 / json tag / 三态必填)。同一份源码两次运行输出字节一致,可进版本控制。

输出为 `contract/contract.json`(schemaVersion 1),顶层字段为 `generator`(提取命令,如 `fasteredge-contract-extract .`)、`stats`(汇总计数)与 `unusedConstants`(未被任何分派分支引用的命令常量,当前为空数组),内容字段含:

- `components[]`:组件名、`kind`(`ability`/`data`)、`source`(源文件名)、`commands[]`(命令名 + `constant` 常量名 + `args`)
- `args.kind`:`struct`(参数结构体) / `none`(分支内显式拒绝非 nil args) / `unknown`(静态不可判定,如 `switch` 之前统一拒绝)
- `args.wrapper`:Go 侧"参数结构体再包一层配置结构体"(`configure` 系列),消费方需知悉这是结构性差异
- `args.fields[]`:字段名、`json` 名、Go 类型、`required`(三态)

`stats` 实测值(2026-11 当次提取):`components: 31`、`commands: 226`、`commandConstants: 198`、`distinctCommandValues: 151`、`argsStruct: 136`、`argsNone: 86`、`argsUnknown: 4`、`fieldsRequired: 10`、`fieldsOptional: 7`、`fieldsUnknown: 234`;这些数字随源码变化,引用时请重新跑一次提取器核对。

提取结果本身由 `cmd/contract/extract_test.go` 守护:组件集合与源码双向比对、每条命令都能在分派里找到(反之亦然)、委托链路与 wrapper 标记按实际源码抽查、两次提取字节一致。这些测试与 `ability/command_contract_test.go`(守护 `ListCommands` 与分派分支集合相等)共同构成"命令目录不许漂移"的两道闸门。

下游比对命令见 Simulator 仓库 README 第九节;**本仓库只承诺"提取结果如实反映 Go 源码",不承诺下游实现与它一致**——那是下游自己差分并负责修的。当前 Go 侧基线:31 个组件 / 226 条分派命令 / 198 个命令常量,无未引用常量。

### 九、仓库其他入口

- `examples/demo`:独立 module(`examples/demo/go.mod`,以 `replace` 指回主库),`go run .` 即跑一遍最小可用流程——挂载常用组件、NetMap 注册对端、OneKey 签发并校验短期令牌、TimeAbility 同步系统时间、ConfigFileAbility 持久化 JSON 配置、ShAbility 执行 `echo`,最后优雅退出。
- `Dockerfile`:多阶段构建,静态编译 `examples/demo` 后落到 `alpine:3.21` 非 root 运行(uid 1000),产物为 `/usr/local/bin/fasteredge-demo`。
- `SECURITY.md`:漏洞私密报告渠道(GitHub Security Advisory / `shun_@outlook.com`)、48 小时确认与 7 天修复计划的响应承诺,以及本项目已落实的加固清单与已知局限(含 `set_allowlist_any` 未入本地限制清单的登记缺口)。
