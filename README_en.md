<div align="center">
  <img src="https://avatars.githubusercontent.com/u/245985800?s=200&v=4" alt="logo" width="100" />
  <h2>FasterEdge</h2>
  <h3>A Symmetric, Reliable and Secure Multi-Scenario Edge Computing Framework</h3>
</div>

### 1. Introduction

- "Imagine each tree as a service (Atom) on a device. The tree has all kinds of roots (Data), and it draws nutrients from the roots and delivers them to the branches (Ability). On the branches there may be some bird nests or beehives — these little creatures carry 'those things' from one tree to another, and on its own tree 'those things' can be accessed by any part."

- Of course this is a tree that can be large or small, and it even supports "hydroponics" to adapt to all kinds of "living environments". You can develop any custom branch (Ability) or root (Data) to achieve a degree of "ultimate optimization" and produce different results.

- You can directly use the Go language implementation to package and deploy on most platforms, or use the Rust language version (not yet released) or C++ language version (not yet released) for higher-performance edge computing services; of course they can also interact with each other.

- This means you can use the "trees" under this framework (that is, this strong specification of "depending on abstractions rather than concrete implementations") to build a fairly flexible distributed edge computing system, or even a distributed edge computing ecosystem.

<div align="center">
  <img src="./concept.png" style="width:96%;" width="100"/>
</div>

- The little girl in the picture represents the business or user (Business/User); these branches (Ability) grow from nutrients supplied by the roots (Data).

### 2. Terminology

- **Atom**: a container of all Data and Ability registered on a node, providing a unified lifecycle.
- **Data**: the "root" on a node, carrying persistent or runtime state (configuration, secrets, topology etc.).
- **Ability**: the "branch" on a node, providing imperative APIs and optional Runner / Unmounter lifecycles.
- **Command**: the unified invocation interface of Ability and Data: `Command(atom, act, args) -> CommandOutput{Name, Value, Err}`.
- **Transport**: an external dependency abstraction injected into Ability (network, MQTT, Docker, K8s etc.), implemented by the user.

### 3. Development Modes

```go
// 1. Minimal startup (BaseData + BaseAbility only)
atom := fasteredge.InitAtom()

// 2. Or register common components at once (NetMap/Time/OneKey/Cmd/Sh/Bash/ConfigFile etc.)
atom := fasteredge.InitStandardAtom()

// 3. Mount (Check + Mount)
if err := fasteredge.PreRunAtom(atom); err != nil { return err }

// 4. Run in a cancellable context (only affects Ability implementing types.Runner)
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

Local trusted code can call Ability directly, for example:

```go
ab, _ := atom.Ability("NetMapAbility")
out := ab.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
    Name: "edge-2", Address: "10.0.0.2:7000", Role: "edge",
})
```

`InitStandardAtom` installs `OneKeyAbility` as the Atom-wide command authenticator. Remote entry points such as HTTP, MQTT and RPC must use the authenticated dispatch interface and must not expose the local trusted `Command` / `CommandContext`:

```go
credential := ability.OneKeyCredential{
    Subject: token.Subject, IssuedAt: token.IssuedAt,
    ExpiresAt: token.ExpiresAt, Signature: token.Signature,
}
out := atom.AuthenticatedCommandContext(
    ctx, credential, "NetMapAbility", ability.NetMapCommandGetTopology, nil,
)
```

Authentication completes before locating the target component; expired, revoked, pre-rotation or tampered tokens are all rejected. The current OneKey token can be reused within its validity period, so remote nodes should use short TTLs and invalidate tokens promptly via revocation or rotation.

### 4. Ecosystem

#### Implemented Data Components

| Name | Function | Key commands |
|------|----------|--------------|
| `BaseData` | Framework metadata (logo, version) | `logo` / `info` |
| `NetMapData` | Local node network topology (node name, NICs, default egress interface) | `info` / `set_node_name` / `interfaces` / `set_default_iface` |
| `KeyringData` | Shared secrets and token table | `status` / `set_secret` / `rotate` / `issue_token` / `revoke_*` |
| `ConfigData` | Flat dotted-path KV configuration | `get` / `set` / `delete` / `list` / `snapshot` |
| `MySQLData` / `PostgreSQLData` | Relational database public connection parameters and independent passwords | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `SQLiteData` | SQLite file, schema, WAL, timeout and pool parameters | `configure` / `get_config` / `status` / `snapshot` |
| `RedisData` | Redis node list, DB, TLS, timeout, pool and independent password | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `MongoDBData` | MongoDB nodes, auth source, replica set, TLS and independent password | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `InfluxDBData` | InfluxDB Endpoint, Org, Bucket, timeout and independent Token | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |

Database passwords and tokens are kept only in the private memory of the Data; `get_config`, `snapshot` and `JSONMarshal` never return them in plaintext. Every change to configuration or secrets increments `DatabaseStatus.Revision`. For example:

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

`InfluxDBAbility` depends on `InfluxDBData`: the old `set_endpoint` / `set_org` / `set_bucket` / `set_token` commands remain compatible, but the configuration and Token actually live in the Data; `Token` returned by `get_config` is always empty. Ping, write and query go through the injected `InfluxTransport`.

#### Implemented Ability Components

| Name | Category | Core capabilities |
|------|----------|-------------------|
| `BaseAbility` | Basic | `list_data_names` / `list_ability_names` |
| `RoleAbility` | Basic | `describe` / `set_role` / `get_role` |
| `TimeAbility` | Basic | `sync_manual` / `sync_system` / `sync_net` / `sync_ntp` / `get_time` / `configure_run` |
| `NetMapAbility` | Basic | `register_peer` / `unregister_peer` / `update_peer` / `list_peers` / `lookup_peer` / `get_topology` |
| `OneKeyAbility` | Basic | `issue_token` / `verify_token` / `revoke_token` / `revoke_all` / `list_tokens` / `status` / `rotate` (HMAC-SHA256) |
| `CloudRoleAbility` | Basic | `describe` / `set_controller` / `register_service` / `set_status` / `heartbeat` (depends on RoleAbility with role=cloud) |
| `EdgeRoleAbility` | Basic | `describe` / `set_zone` / `add_capability` / `record_latency` / `get_metrics` / `set_online` (depends on RoleAbility with role=edge) |
| `CmdAbility` | Terminal | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `clear_finished` (pluggable allowlist) |
| `ShAbility` | Terminal | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` (based on CmdAbility, `sh -c` form) |
| `BashAbility` | Terminal | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` (based on ShAbility, `bash --noprofile --norc -c` form) |
| `ConfigFileAbility` | File/Config | `set_path` / `load` / `save` / `exists` (JSON persistence based on ConfigData) |
| `FileTransferAbility` | File/Config | `set_target` / `upload` / `download` / `list` / `get_transfer` / `cancel` (pluggable Transport) |
| `AlgorithmDistributionAbility` | File/Config | `register_algorithm` / `unregister_algorithm` / `distribute` / `list_distributions` / `cancel` (based on FileTransfer) |
| `ModbusAbility` | Industrial protocols | `set_endpoint` / `set_unit_id` / `read_holding` / `read_input` / `read_coils` / `read_discrete` / `write_*` (pluggable) |
| `SerialAbility` | Industrial protocols | `open` / `close` / `read` / `write` / `set_config` / `list_ports` (pluggable Transport) |
| `TSNAbility` | Industrial protocols | `set_interface` / `register_talker` / `register_listener` / `unregister` / `set_priority_map` / `set_time_aware` |
| `MQTTAbility` | Data exchange | `set_broker` / `connect` / `disconnect` / `publish` / `subscribe` / `drain` / `list_subscriptions` (pluggable Transport) |
| `InfluxDBAbility` | Data exchange | `set_endpoint` / `set_token` / `set_org` / `set_bucket` / `ping` / `write` / `query` / `list_series` / `delete_series` |
| `EKuiperAbility` | Data exchange | `set_endpoint` / `create_stream` / `drop_stream` / `create_rule` / `start_rule` / `stop_rule` / `show_rules` |
| `DockerAbility` | Container orchestration | `set_endpoint` / `list_containers` / `start/stop/restart/remove` / `pull_image` / `inspect` / `get_logs` / `create` |
| `KubernetesAbility` | Container orchestration | `set_context` / `apply` / `delete` / `list` / `get` / `scale` / `get_logs` |

All capabilities that depend on external networks/processes (FileTransfer / Modbus / Serial / MQTT / InfluxDB / EKuiper / Docker / Kubernetes) are injected via `SetXxxTransport(...)`; the framework itself only manages metadata and lifecycle, without binding to any concrete driver.

#### Cross-Capability Composition Example

```go
atom := fasteredge.InitStandardAtom()
fasteredge.PreRunAtom(atom)

// 1. Register a peer node via NetMap
nm, _ := atom.Ability("NetMapAbility")
nm.Command(atom, ability.NetMapCommandRegisterPeer, ability.NetMapRegisterPeerArgs{
    Name: "edge-2", Address: "10.0.0.2:7000", Role: "edge",
})

// 2. Issue a short-lived token for it via OneKey
ok, _ := atom.Ability("OneKeyAbility")
out := ok.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
    Subject: "edge-2", TTL: time.Hour,
})
tok := out.Value.(ability.OneKeyToken)

// 3. The remote side verifies with verify_token
ok.Command(atom, ability.OneKeyCommandVerifyToken, ability.OneKeyVerifyTokenArgs{
    Subject: tok.Subject, IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt, Signature: tok.Signature,
})
```

### 5. Design Philosophy
- Depend on Abstractions, Not on Concrete Implementations.
- Follows the Strategy Pattern, Command Pattern and Composite Pattern
- All command arguments are strictly typed; any nil / type mismatch / blank value returns `types.ErrInvalidArguments`
- Internal state is fully protected by `sync.RWMutex` / `atomic`; `-race` clean
- Ability involving external networks rejects `localhost` / `127.0.0.1` / `0.0.0.0` / `::1` by default to reduce SSRF risk

### 6. Lifecycle and Graceful Shutdown

FasterEdge is a library, not a standalone process. First register components, then mount, then run in a cancellable context:

```go
atom, err := fasteredge.InitAtom()
if err != nil { return err }
if err := fasteredge.PreRunAtom(atom); err != nil { return err }
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

`RunAtom` supervises Ability implementing `types.Runner`; after context cancellation or a Runner returning an error, the framework unmounts components in reverse order using a fresh cleanup context. Shutdown timeout, panics and unmount errors are all returned as structured `error`; the component registry never exposes its internal map.

TimeAbility command arguments use strict types (e.g. `TimeSyncManualArgs`), and the time cache advances based on the monotonic clock. Network time sources reject localhost, private, link-local and multicast addresses by default and disable the environment proxy; only when the LAN source option is explicitly enabled are private addresses allowed. HTTP responses are limited to 64 KiB, and NTP uses the validated response offset.

### 7. Tests

```bash
go test ./...           # Run all unit tests
go test -race ./...     # With race detection
go test ./... -v -run Integration   # Cross-capability integration tests
```

Current coverage: **~135 tests, all passing, `go vet` with no warnings.**