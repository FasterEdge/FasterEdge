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

// 2. Or register common components at once (identity/role/network/time/crypto/terminal/file-config + 6 DB Data)
atom := fasteredge.InitStandardAtom()

// 3. Mount (Check + Mount)
if err := fasteredge.PreRunAtom(atom); err != nil { return err }

// 4. Run in a cancellable context (only affects Ability implementing types.Runner)
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

What `InitStandardAtom` actually registers (`init.go`) — Data side: `NetMapData` / `KeyringData` / `ConfigData` / `MySQLData` / `PostgreSQLData` / `SQLiteData` / `RedisData` / `MongoDBData` / `InfluxDBData`; Ability side: `RoleAbility` / `TimeAbility` / `NetMapAbility` / `OneKeyAbility` / `CmdAbility` / `ShAbility` / `BashAbility` / `ConfigFileAbility`, plus `BaseData` + `BaseAbility` from `InitAtom`. Capabilities such as Docker / K8s / MQTT must be registered by the caller; the six DB Data components are already part of the skeleton, so do not register them twice.

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

Authentication and authorization both complete before locating the target component; expired, revoked, pre-rotation or tampered tokens are all rejected, and so are calls outside the token's scope (returned as `types.ErrAuthorizationDenied`, distinguishable from the credential error `types.ErrAuthenticationFailed`).

Tokens can be bound to a capability scope (component + command whitelist, optional args fingerprint), declared at issue time:

```go
out := ok.Command(atom, ability.OneKeyCommandIssueToken, ability.OneKeyIssueTokenArgs{
    Subject: "edge-2", TTL: time.Hour,
    Scope: []data.KeyringScopeRule{
        {Component: "NetMapAbility", Commands: []string{ability.NetMapCommandGetTopology}},
        {Component: "KeyringData", Commands: []string{data.KeyringCommandListTokens}},
    },
})
```

The scope and nonce are covered by the HMAC payload, so a remote peer that tampers with the credential's `Scope` field (widening the whitelist, stripping it, editing the command list) fails signature verification. A token with an empty scope is unrestricted — that is the legacy format's semantics; new issues should declare a scope explicitly.

One-shot tokens (replay protection) are issued with `OneShot: true`: each authentication consumes one nonce and any replay is rejected. Tokens without `OneShot` remain reusable within their validity period (unchanged behaviour), so those should use short TTLs and be invalidated promptly via revocation or rotation. Authentication failures are rate limited per subject (10 per minute by default); beyond that the subject's attempts are rejected.

Boundary notes: `verify_token` is the command the authentication protocol itself depends on and is always allowed; management commands such as `issue_token` / `revoke_*` / `rotate` / `set_secret` / `set_allowlist` / `set_role` are restricted to local in-process calls regardless of token scope.

#### Observability

Every lifecycle phase (`check` / `mount` / `run` / `unmount` / `rollback`) and the command dispatch path (`command`) emit instrumentation events. Attaching them takes one line:

```go
// Metrics only
metrics, err := fasteredge.WithMetrics(atom)
if err != nil { return err }

// Or metrics + a custom sink (logging, alerting, ...)
metrics, err := fasteredge.WithEventSinkAndMetrics(mySink, atom)
if err != nil { return err }

defer func() {
    // Prometheus text format (text/plain; version=0.0.4), or metrics.WriteJSON()
    fmt.Print(metrics.WritePrometheus())
}()
```

The instrumentation contract is "one event pair per phase" (`start` + `success`/`failure`). Unmount and rollback timeouts also produce a terminal event, so a hung component is observable — it never leaves a bare `start` behind. The export channel is yours to choose: the framework starts no HTTP server and writes no files, it only provides renderers.

```text
fasteredge_lifecycle_events_total{phase="mount",status="success"} 19
fasteredge_phase_duration_seconds_count{phase="mount"} 19
fasteredge_phase_duration_seconds{phase="mount",quantile="0.99"} 0.1067416
```

`types.MetricsCollector` implements `types.EventSink`, so it can be fed into `types.NewMultiEventSink` alongside other sinks. If you only need raw events without aggregation, use `types.EventRecorder`.

### 4. Ecosystem

#### Implemented Data Components

| Name | Function | Key commands |
|------|----------|--------------|
| `BaseData` | Framework metadata (logo, version) | `logo` / `info` |
| `NetMapData` | Local node network topology (node name, NICs, default egress interface) | `info` / `set_node_name` / `interfaces` / `set_default_iface` |
| `KeyringData` | Shared secrets and token table | `status` / `set_secret` / `rotate` / `issue_token` / `list_tokens` / `revoke_token` / `revoke_all` |
| `ConfigData` | Flat dotted-path KV configuration | `get` / `set` / `delete` / `list` / `snapshot` |
| `MySQLData` / `PostgreSQLData` | Relational database public connection parameters and independent passwords | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `SQLiteData` | SQLite file, schema, WAL, timeout and pool parameters | `configure` / `get_config` / `status` / `snapshot` |
| `RedisData` | Redis node list, DB, TLS, timeout, pool and independent password | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `MongoDBData` | MongoDB nodes, auth source, replica set, TLS and independent password | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |
| `InfluxDBData` | InfluxDB Endpoint, Org, Bucket, timeout and independent Token | `configure` / `set_secret` / `clear_secret` / `get_config` / `status` / `snapshot` |

Database passwords and tokens are kept only in the private memory of the Data; `get_config`, `snapshot` and `JSONMarshal` never return them in plaintext. Every change to configuration or secrets increments `DatabaseStatus.Revision`. Database endpoints (including `InfluxDBData`) go through the same unified address policy described in section 5: SQL/Redis/MongoDB hosts use the `AllowPrivate` level (private networks are legal edge topology), while the InfluxDB endpoint uses `PublicOnly`. For example:

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

Command names below are the literals reported by `go run ./cmd/contract -repo . -stdout`; every row matches a dispatch branch in the source 1:1.

| Name | Category | Core capabilities |
|------|----------|-------------------|
| `BaseAbility` | Basic | `list_data_names` / `list_ability_names` |
| `RoleAbility` | Basic | `describe` / `set_role` / `get_role` |
| `TimeAbility` | Basic | `sync_manual` / `sync_system` / `sync_net` / `sync_ntp` / `get_time` / `configure_run` / `last` |
| `NetMapAbility` | Basic | `register_peer` / `unregister_peer` / `update_peer` / `list_peers` / `lookup_peer` / `get_topology` |
| `OneKeyAbility` | Basic | `issue_token` / `verify_token` / `revoke_token` / `revoke_all` / `list_tokens` / `status` / `rotate` (HMAC-SHA256; tokens can bind a component/command scope and a one-shot nonce) |
| `CloudRoleAbility` | Basic | `describe` / `set_controller` / `get_controller` / `register_service` / `unregister_service` / `list_services` / `set_status` / `get_status` / `heartbeat` (depends on RoleAbility with role=cloud) |
| `EdgeRoleAbility` | Basic | `describe` / `set_zone` / `get_zone` / `set_capabilities` / `add_capability` / `remove_capability` / `list_capabilities` / `record_latency` / `get_metrics` / `set_online` (depends on RoleAbility with role=edge) |
| `CmdAbility` | Terminal | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `get_allowlist` / `set_allowlist_any` / `get_job` / `clear_finished` (pluggable allowlist, **fail-closed**) |
| `ShAbility` | Terminal | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `get_allowlist` / `set_allowlist_any` (based on CmdAbility, `sh -c` form; does not inherit `get_job` / `clear_finished`, **fail-closed**) |
| `BashAbility` | Terminal | `run` / `start` / `wait` / `kill` / `list` / `set_allowlist` / `get_allowlist` / `set_allowlist_any` (based on ShAbility, `bash --noprofile --norc -c` form; does not inherit `get_job` / `clear_finished`, **fail-closed**) |
| `ConfigFileAbility` | File/Config | `set_path` / `get_path` / `load` / `save` / `exists` (JSON persistence based on ConfigData) |
| `FileTransferAbility` | File/Config | `set_target` / `get_target` / `upload` / `download` / `list` / `get_transfer` / `cancel` / `clear_finished` (pluggable Transport) |
| `AlgorithmDistributionAbility` | File/Config | `register_algorithm` / `unregister_algorithm` / `get_algorithm` / `list_algorithms` / `distribute` / `cancel` / `list_distributions` / `clear_finished` (based on FileTransfer) |
| `ModbusAbility` | Industrial protocols | `set_endpoint` / `get_endpoint` / `set_unit_id` / `get_unit_id` / `read_holding` / `read_input` / `read_coils` / `read_discrete` / `write_holding` / `write_coil` / `write_holding_multi` (pluggable) |
| `SerialAbility` | Industrial protocols | `open` / `is_open` / `close` / `read` / `write` / `set_config` / `get_config` / `list_ports` (pluggable Transport) |
| `TSNAbility` | Industrial protocols | `set_interface` / `get_interface` / `register_talker` / `register_listener` / `unregister` / `list_streams` / `set_priority_map` / `get_priority_map` / `set_time_aware` / `get_time_aware` (pluggable `TSNTransport`) |
| `MQTTAbility` | Data exchange | `set_broker` / `get_broker` / `set_client_id` / `set_credentials` / `connect` / `disconnect` / `is_connected` / `publish` / `subscribe` / `unsubscribe` / `drain` / `list_subscriptions` (pluggable Transport) |
| `InfluxDBAbility` | Data exchange | `set_endpoint` / `get_endpoint` / `set_token` / `set_org` / `set_bucket` / `get_config` / `ping` / `write` / `query` / `list_series` / `delete_series` (configuration and Token actually live in `InfluxDBData`) |
| `EKuiperAbility` | Data exchange | `set_endpoint` / `get_endpoint` / `create_stream` / `drop_stream` / `get_stream` / `list_streams` / `create_rule` / `start_rule` / `stop_rule` / `get_rule_status` / `drop_rule` / `show_rules` |
| `DockerAbility` | Container orchestration | `set_endpoint` / `get_endpoint` / `list_containers` / `start_container` / `stop_container` / `restart_container` / `remove_container` / `create_container` / `inspect_container` / `pull_image` / `get_logs` |
| `KubernetesAbility` | Container orchestration | `set_context` / `get_context` / `apply` / `delete` / `list` / `get` / `scale` / `get_logs` |

All capabilities that depend on external networks/processes (FileTransfer / AlgorithmDistribution / Modbus / Serial / TSN / MQTT / InfluxDB / EKuiper / Docker / Kubernetes) are injected through their own `SetTransport(...)` — one method per ability, not a per-driver `SetXxxTransport(...)` family. Injection points: `serial_ability.go:101`, `tsn_ability.go:164`, `mqtt_ability.go:216`, `modbus_ability.go:108`, `docker_ability.go:104`, `kubernetes_ability.go:145`, `ekuiper_ability.go:116`, `influxdb_ability.go:96`, `file_transfer_ability.go:183` (forwarded by `algorithm_distribution_ability.go:174`). The framework itself only manages metadata and lifecycle, without binding to any concrete driver.

#### SerialAbility: real backends and scope

`SerialAbility` opens no device files itself: the byte stream must go through a `SerialTransport` injected with `SetTransport(...)`; the framework only tracks port state and config records. **Without a transport, `open` returns an error (`no transport set`) instead of silently succeeding**; `is_open` / `get_config` / `list_ports` only read back local records and work offline.

`examples/verify/serial_transport.go` is a **minimal but real** reference implementation (verification program only, not a library dependency). That file itself carries no build tag; the GOOS split lives in three sibling files: `serial_port_windows.go` (`//go:build windows`), `serial_port_linux.go` (`//go:build linux`) and `serial_port_unsupported.go` (`//go:build !windows && !linux`):

- **Windows**: `CreateFile("\.\COMx")` + `SetCommState` (DCB baud/data bits/stop bits/parity) + `SetCommTimeouts` (where the read timeout actually lands); port enumeration reads `HARDWARE\DEVICEMAP\SERIALCOMM`.
- **Linux**: `open("/dev/ttyUSB*")` + termios `TCGETS`/`TCSETS` + `select` for read timeouts; port enumeration scans `ttyS*`/`ttyUSB*`/`ttyACM*` under `/dev`.
- **Other platforms** (darwin/wasm, ...): `serial_port_unsupported.go` returns an explicit "not implemented" error so `go build ./...` passes on any GOOS instead of pretending to work. Verified on this checkout with `GOOS/GOARCH` set to `darwin/arm64`, `linux/arm64`, `windows/arm64` and `js/wasm` — all four build with exit 0.

`examples/verify` injects it and runs a real round trip: `open → is_open → write → get_config → close`. Measured on this build machine (`go run .` inside `examples/verify`), the `Serial/real-*` assertions all PASS against the enumerated port `COM1` (`real-write` reports 9 bytes, `real-get_config` reports `115200-8N1`). That section nevertheless requires a physical serial device and uses the first enumerated port (`ports[0]`) rather than a hard-coded name, so it is **not reproducible from the repository alone**; when no device is available it is explicitly SKIPped (`[SKIP] Serial/real-roundtrip` in `verify_skeleton.go`) instead of being reported as a pass. `ability/serial_ability_test.go` covers the state machine and `examples/verify/serial_transport_test.go` covers the real backend's error paths — those tests need no hardware and are part of `go test ./...`.

`TSNAbility` does not call netlink/tc/taprio itself: interface probing, stream rule installation/removal, PCP-to-queue mapping and Qbv gate control are all delegated to the injected `TSNTransport`. **Without a transport, `set_interface` / `register_talker` / `register_listener` / `unregister` / `set_priority_map` / `set_time_aware` all return an error and never report silent success**; `get_interface` / `list_streams` / `get_priority_map` / `get_time_aware` only read back local records and work offline. When the device-side push fails the local state does not advance (e.g. a failed `unregister` keeps the entry so the caller can retry).

#### Command execution security model (fail-closed)

`CmdAbility` / `ShAbility` / `BashAbility` are **fail-closed**: execution is denied by default, not allowed by default.

- **An empty allowlist denies every execution.** `run` / `start` return `ErrAllowlistEmpty` (wrapped in `types.ErrInvalidArguments`) when no allowlist has been configured, and the message names the opt-in paths. The previous behaviour — "empty allowlist means unrestricted" — was a default-open authorization hole and has been removed.
- **Two explicit ways to grant execution**: (1) `set_allowlist` lists the permitted commands (exact executable name plus args prefix for `CmdAbility`; first word for `ShAbility` / `BashAbility`); (2) `set_allowlist_any` (`CmdSetAllowAnyArgs{Any:true}`, or the Go API `SetAllowAny(true)`) opens "allow any command". Opening it is the caller's explicit decision; the framework never makes it for you.
- **Remote surface (one known gap)**: `set_allowlist` grants capability, so `OneKeyAbility`'s authentication boundary restricts it to local in-process calls; `run` / `start` stay remotely reachable but always subject to the allowlist. **The new `set_allowlist_any` is not yet in that local-only list** — it needs one more case in `restrictedRemoteCommand` inside `ability/one_key_ability.go`, a file another owner was refactoring in parallel, so this change deliberately left it untouched; the gap is recorded by `TestCmdAbilitySetAllowAnyRemoteRestriction`. Do not expose `set_allowlist_any` to remote transports until that line lands.
- **Composite-command blocking (honest scope, not exhaustive)**: after the first word matches, shell chaining metacharacters (`;` `&` `|` `$()` backticks `` ` `` process substitution `<(` `>()` `|&` `${` and control characters such as CR/LF) are scanned for and the whole command is rejected. This is a **quote-aware character scan, not a shell parser**: backslash-escaped encodings (e.g. `\x3b` for `;`) are not decoded, and callers must rely on fail-closed defaults or tighter allowlists there. Boundary cases live in `TestShAbilityRejectsExtendedChainingBypass` in `ability/sh_ability_test.go`.

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
- Capabilities that touch external networks reject `localhost` / `127.0.0.1` / `0.0.0.0` / `::1` by default to reduce SSRF risk. Address decisions are unified in `ability/address_policy.go` — an alias layer whose implementation lives in `types/address_policy.go`, so `data/` can reuse the same rules without an import cycle (`AddressPolicyLevel`: `PublicOnly` / `AllowPrivate` / `AllowLoopback`). Each capability picks a level explicitly instead of writing its own rule set. IPv4-mapped IPv6 forms (`::ffff:127.0.0.1`), userinfo tricks (`tcp://alice@host:port`) and decimal-notation IPs (`2130706433`, `127.1`, `0x7f.0.0.1`) are always rejected; trailing-dot FQDNs (`host.example.com.`) are accepted as hostnames.
- **What the dial-time re-check actually covers (stated honestly)**: today only `TimeAbility` re-checks the **actually resolved IPs** at dial time, which is the anti-DNS-rebinding defence — `ability/time_http.go:28` installs an `AddressGuard` into `http.Transport.DialContext`, `ability/time_ntp.go:57` validates every resolved IP (`p.resolve`) before `:74` dials the numeric addresses, and `ability/time_address.go:59` is the shared `AddressGuard.DialContext`. The other capabilities (Docker / MQTT / K8s / InfluxDB / EKuiper, plus the DB Data endpoints) perform the **storage-time** syntax/literal check only (`docker_ability.go:374`, `mqtt_ability.go:622`, `kubernetes_ability.go:475` and `:497`, `influxdb_ability.go:32`, `ekuiper_ability.go:186`, and `data/database_data.go` `validateDatabaseHost` (`:130`) / `validateAddress` (`:153`) / `validateInfluxEndpoint` (`:362`), which all delegate to the shared policy); re-checking resolved IPs at dial time is the injected transport's responsibility, because the framework does not dial on their behalf. Therefore this README does **not** claim DNS-rebinding protection for every capability.
- **`localhost` is opt-in, per capability.** `ModbusAbility.set_endpoint` accepts RTU device paths (`/dev/ttyUSB0:9600:8N1`) and TCP endpoints; TCP endpoints default to the `AllowPrivate` level (industrial slaves usually live on 10.x/172.16-31.x/192.168.x), and `127.0.0.1:502` is rejected until the caller explicitly opens it with `SetAllowLoopback(true)` — the same discipline as TimeAbility's LAN-source option. `FileTransferAbility` guards only the **target peer address** (from `NetMapAbility`, `AllowPrivate`); `LocalPath` / `RemotePath` are file paths and are deliberately not passed through the address policy.

### 6. Lifecycle and Graceful Shutdown

FasterEdge is a library, not a standalone process. First register components, then mount, then run in a cancellable context:

```go
atom := fasteredge.InitAtom()   // single return value, no error
if err := fasteredge.PreRunAtom(atom); err != nil { return err }
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

`RunAtom` supervises Ability implementing `types.Runner`; after context cancellation or a Runner returning an error, the framework unmounts components in reverse order using a fresh cleanup context. Shutdown timeout, panics and unmount errors are all returned as structured `error`; the component registry never exposes its internal map. Three read-only runtime queries are available: `atom.CommandNames()` (the command catalogue each component declared), `atom.Status()` (a lifecycle snapshot) and `atom.Health(ctx)` (per-component health aggregation, 2-second timeout per check); `atom.Reset()` puts a `Stopped`/`Failed` Atom back into `Created` so it can be mounted again (registered components are kept, only the mount bookkeeping is cleared).

TimeAbility command arguments use strict types (e.g. `TimeSyncManualArgs`), and the time cache advances based on the monotonic clock. Network time sources reject localhost, private, link-local and multicast addresses by default and disable the environment proxy; only when the LAN source option is explicitly enabled are private addresses allowed. HTTP responses are limited to 64 KiB, and NTP uses the validated response offset.

### 7. Tests

```bash
go test ./...           # Run all unit tests
go test -race ./...     # With race detection
go test ./... -v -run 'Integration|Combination|Batch6|Smoke'   # cross-capability integration / combination suites
```

`-run Integration` alone matches only 3 functions (1 of which SKIPs); `TestSmokeEveryAbility`, the 11 `TestCombination*` functions and the 9 `TestBatch6*` functions do not match it, hence the regex above. Measured on this checkout: that regex runs **24 top-level cases (22 PASS / 2 SKIP)** — both SKIPs are `TestIntegrationShBashEcho` and `TestCombinationTimeSh`, which skip themselves on Windows (`no POSIX shell on this platform` / `POSIX-only`); on Linux/macOS they run. It covers the full-skeleton lifecycle, DB Data composition and atomicity, Influx Data↔Ability wiring, the token authentication chain, and the NetMap / ConfigFile / FileTransfer / Time+Sh cross-capability combinations.

Beyond unit and integration tests, `ability/fuzz_test.go` defines **8 fuzz targets** (`FuzzShellQuote` / `FuzzIsValidMAC` / `FuzzIsValidSerialPort` / `FuzzIsValidModbusEndpoint` / `FuzzDecodeFromTransmission` / `FuzzIsAcceptableBrokerURL` / `FuzzIsAcceptableControllerURL` / `FuzzModbusEncodingRoundTrip`) and `ability/bench_test.go` defines **8 benchmarks** (`BenchmarkRoleAbilitySetGet` / `BenchmarkKeyringDataIssueAndVerify` / `BenchmarkNetMapAbilityRegisterLookup` / `BenchmarkConfigDataGetSet` / `BenchmarkModbusReadHolding` / `BenchmarkMQTTPublishDrain` / `BenchmarkFullAtomInitStandardAtom` / `BenchmarkAtomMountAndUnmount`). Run the benchmarks with `go test -run '^$' -bench=. -benchmem ./ability` (benchmarks only, tests skipped) and a chosen fuzz target with `go test -fuzz=FuzzXxx -fuzztime=Ns ./ability`; plain `go test ./...` only runs each fuzz target's seed corpus once.

Current coverage (measured on this checkout, `go test ./... -list 'Test.*' | grep -c '^Test'`): **all unit and integration tests pass, `go vet` reports nothing, `-race` clean** — all 6 packages `ok`. Test functions: **415**. The number changes as components are added, so re-measure before quoting it.

### 8. Command-Contract Extraction and Ecosystem Diffing

The ecosystem contains second implementations (such as the frontend registry of [Simulator](https://github.com/FasterEdge/Simulator)) that align their command names with the Go side by hand. To turn "aligned" into a machine-checkable fact, this repository ships a static contract extractor:

```bash
go run ./cmd/contract -out contract/contract.json   # or -stdout to print directly
```

The extractor uses `go/ast` to parse the `Command()` of every component under `ability/` and `data/`: it walks each dispatch branch of the `switch act` (including branches reached through helpers such as `simpleContainerAction(act, args)`, `sqlCommand(store, act, args)` and `b.sh.Command(mapBashCommand(act), args)`), resolves the case label's constant identifier against the consts in the same file to recover the command literal, and then parses the argument struct asserted by `args.(XxxArgs)` in that branch (name / type / json tag / three-valued requiredness). Two runs over the same source produce byte-identical output, so the file can be version-controlled.

The output is `contract/contract.json` (schemaVersion 1). Top-level fields are `generator` (the extraction command, e.g. `fasteredge-contract-extract .`), `stats` (aggregate counts) and `unusedConstants` (command constants not referenced by any dispatch branch — currently an empty array). The content fields are:

- `components[]`: component name, `kind` (`ability` / `data`), `source` (file name), and `commands[]` (command name + `constant` name + `args`)
- `args.kind`: `struct` (argument struct) / `none` (the branch explicitly rejects non-nil args) / `unknown` (not decidable statically, e.g. a uniform rejection before the `switch`)
- `args.wrapper`: the Go side wraps a configuration struct in the argument struct (the `configure` family), which consumers must treat as a structural difference
- `args.fields[]`: field name, `json` name, Go type, and three-valued `required`

`stats` as measured by the current extraction: `components: 31`, `commands: 226`, `commandConstants: 198`, `distinctCommandValues: 151`, `argsStruct: 136`, `argsNone: 86`, `argsUnknown: 4`, `fieldsRequired: 10`, `fieldsOptional: 7`, `fieldsUnknown: 234`. These numbers move with the source — re-run the extractor before quoting them.

The extraction is guarded by `cmd/contract/extract_test.go`: a two-way comparison of the component set against the source, every command findable in a dispatch branch (and vice versa), the delegation chains and wrapper flags spot-checked against the real source, and two extractions producing identical bytes. Together with `ability/command_contract_test.go` (which guards that `ListCommands` equals the set of dispatch branches) these form two gates against command-catalog drift.

For the downstream diff command see section 9 of the Simulator README; **this repository only promises that the extraction faithfully reflects the Go source, not that downstream implementations match it** — that is for each downstream to diff and fix on its own. Current Go-side baseline: 31 components / 226 dispatched commands / 198 command constants, no unreferenced constants.

### 9. Other Repository Entry Points

- `examples/demo`: a separate module (`examples/demo/go.mod`, pointing back at the main library through `replace`). `go run .` walks through a minimal working flow: mount the common components, register a NetMap peer, issue and verify a short-lived OneKey token, sync system time, persist a JSON config through ConfigFileAbility, run `echo` through ShAbility's allowlist, then shut down gracefully.
- `Dockerfile`: multi-stage build that statically compiles `examples/demo` and runs it as a non-root user (uid 1000) on `alpine:3.21`, producing `/usr/local/bin/fasteredge-demo`.
- `SECURITY.md`: the private vulnerability-report channel (GitHub Security Advisory / `shun_@outlook.com`), the 48-hour acknowledgement and 7-day fix-response commitments, plus the list of hardening already applied and the known limitations (including the registered `set_allowlist_any` local-only gap).
