# FasterEdge 核心加固设计

**日期：** 2026-08-09

**状态：** 设计已确认，待用户审阅文档

**范围：** Atom 内核、组件生命周期、命令错误模型、内置 Ability/Data 和测试体系

## 1. 背景

FasterEdge 当前以 `Atom` 聚合 `Data` 和 `Ability`，通过统一的字符串命令扩展行为。原型已经表达了“依赖抽象”的方向，但注册、挂载和运行的边界尚未真正建立：挂载失败的组件仍留在运行集合，Atom 值传递在 nil map 与非 nil map 下表现不一致，注册表直接暴露且没有并发保护，`RunAtom` 依赖永久 `select {}` 保活，运行错误无法传播，也无法优雅退出。

时间能力还存在系统壁钟跳变、首次同步覆盖竞态、HTTP 请求无超时与响应上限、可控地址产生 SSRF 风险、CPU ticker 无法停止等问题。当前测试依赖公网 NTP、没有断言，无法证明这些行为正确。

本轮允许破坏现有公开 API，以正确性和可维护性优先。仍保留统一 `Command` 入口，不在本轮实现跨设备通信、服务发现或持久化。

## 2. 目标与非目标

### 2.1 目标

- 让 Atom 的注册、检查、挂载、运行和停止具有明确且可验证的状态语义。
- 支持需要长期在线运行的 Ability，并允许外部取消、组件故障联动停止和错误回传。
- 私有化注册表，消除运行期并发 map 读写风险和同名静默覆盖。
- 隔离生命周期回调中的组件 panic，避免单个插件直接终止整个进程。
- 保留统一命令协议，同时严格校验参数并保留原始错误。
- 让 TimeAbility 在系统壁钟跳变、并发同步和恶意/异常网络响应下行为可控。
- 让全部测试离线、确定且可在 race detector 下重复执行。
- 补充最小可用文档，展示注册、挂载、持续运行和优雅退出。

### 2.2 非目标

- 不实现 Unix Socket、TCP、消息总线或跨设备数据同步。
- 不实现组件依赖图、自动拓扑排序、热插拔或运行后动态注册。
- 不实现进程管理、守护进程、容器镜像或部署系统。
- 不承诺兼容当前原型 API；迁移方式在 README 中明确说明。

## 3. 方案选择

采用“结构化生命周期”方案，而不是对现有代码做局部修补，也不引入完整 Runtime 引擎。

- 局部修补改动较少，但会继续保留注册/挂载混用以及 `runnable`、`run`、`blocking` 等魔法命令。
- 完整 Runtime 引擎可加入依赖图和监督策略，但超出当前 666 行原型的实际需要。
- 结构化生命周期以少量新接口建立正确边界，同时保留现有 Atom/Data/Ability 心智模型。

## 4. 核心 API

根包生命周期 API 调整为：

```go
func InitAtom() *types.Atom
func PreRunAtom(atom *types.Atom) error
func RunAtom(ctx context.Context, atom *types.Atom, opts ...RunOption) error
```

`RunAtom` 默认关闭等待时间为 5 秒，并提供 `WithShutdownTimeout` 调整。nil Atom、非法状态和非正关闭时间均返回明确错误。

组件接口共享以下约定：

```go
type Component interface {
    GetName() string
    Describe() string
    Check(*Atom) error
    Mount(*Atom) error
    Command(*Atom, string, any) CommandOutput
}

type Data interface {
    Component
}

type Ability interface {
    Component
}

type Runner interface {
    Run(context.Context, *Atom) error
}

type Unmounter interface {
    Unmount(context.Context, *Atom) error
}
```

`Runner` 是长期运行能力的显式契约。一次性操作仍由 `Command` 完成。`Unmounter` 是可选的资源回收契约；已挂载组件在启动失败或运行结束后按挂载顺序逆序卸载。

## 5. Atom 状态与注册表

Atom 使用以下单向状态：

```text
Created --pre-run success--> Mounted --run--> Running --clean exit--> Stopped
   |                            |                 |
   +-- check/mount failure -----+                 +-- shutdown/cleanup failure
                    |                                      |
                    +----------------> Failed <-------------+
```

- `Created`：允许设置名称、注册 Data 和 Ability。
- `Mounted`：全部依赖检查及挂载成功；注册表被冻结。
- `Running`：Runner 已由监督器启动。
- `Stopped`：全部 Runner 已退出且资源清理完成，不代表返回错误一定为 nil。
- `Failed`：检查/挂载失败，或停止、卸载未能完整完成。若关闭超时，可能仍有违反契约的 Runner 存活。

第一阶段不支持从 `Stopped` 或 `Failed` 重新运行，也不支持 `Mounted` 或 `Running` 状态下新增组件。

Atom 内部维护候选注册表、成功挂载快照、Ability 专用运行快照、挂载顺序、状态和 `sync.RWMutex`。Data 与 Ability 在 Go 中具有相同的方法集，运行快照必须显式保留类别，不能在挂载后通过类型断言反推。所有 map 均为私有字段。公开读取 API 返回单项值或 map 副本，不泄露可写底层 map。

```go
func (a *Atom) AddData(Data) error
func (a *Atom) AddAbility(Ability) error
func (a *Atom) Data(name string) (Data, bool)
func (a *Atom) Ability(name string) (Ability, bool)
func (a *Atom) AllData() map[string]Data
func (a *Atom) AllAbilities() map[string]Ability
func (a *Atom) State() AtomState
```

注册拒绝 nil、带类型 nil、空白名称、带首尾空白的名称和同名组件；不再静默覆盖。名称保存原始大小写并作为键，`PreRunAtom` 会与注册值精确复核；任何变化都返回 `ErrComponentNameChanged`。注册和复核期间调用 `GetName` 发生的 panic 也转换为 `ComponentPanicError`。

## 6. 预运行与挂载

`PreRunAtom` 执行以下流程：

1. 在锁内确认 Atom 处于 `Created`，复制候选组件快照后释放锁。
2. 按“全部 Data、全部 Ability”的稳定名称顺序调用 `Check`。`Check` 必须无副作用。
3. 任一检查失败时使用 `errors.Join` 汇总全部失败，Atom 转为 `Failed`，不调用任何 `Mount`。
4. 全部检查成功后，按相同稳定顺序调用 `Mount`。
5. 某次挂载失败时，逆序调用此前已挂载且实现 `Unmounter` 的组件，汇总挂载和清理错误，Atom 转为 `Failed`。
6. 全部挂载成功后保存不可变运行快照和挂载顺序，Atom 转为 `Mounted`。

`Mount` 不再把自身重新加入 Atom；注册和挂载是两个独立阶段。单个 `Mount` 必须保证自身失败时清理它在本次调用中创建的部分资源。

框架调用 `Check`、`Mount` 和 `Unmount` 时会恢复 panic，并转换为包含组件名称、阶段、panic 值和堆栈的 `ComponentPanicError`。panic 与普通错误使用同一汇总和回滚路径。

## 7. 持续运行与优雅退出

`RunAtom` 不再依赖 `BaseAbility.Command("blocking")`。它本身是阻塞式监督器：

1. 确认 Atom 处于 `Mounted`，切换为 `Running`。
2. 从成功挂载的 Ability 快照中筛选 `Runner`，每个 Runner 使用独立 goroutine 执行。
3. 长期在线 Ability 的 `Run` 应持续工作，直到 context 取消或遇到不可恢复错误。
4. 任一 Runner 返回非 context 错误时，监督器记录组件名称和原始错误，并取消其他 Runner。
5. Runner goroutine 的 panic 被恢复为 `ComponentPanicError`，按 Runner 错误处理，不允许传播到进程顶层。
6. 外部 context 取消或截止时，监督器把取消信号传播给全部 Runner。
7. 监督器等待 Runner 退出；超过关闭等待时间则返回 `ErrShutdownTimeout`。Go 无法强制杀死 goroutine，因此 Runner 必须响应 context，超时错误用于暴露违反契约的实现。
8. 只有 Runner 全部退出后，才使用独立的新清理 context 逆序卸载实现 `Unmounter` 的组件，不能复用已取消的 Runner context。清理完整时 Atom 转为 `Stopped`；卸载失败或超时时转为 `Failed` 并返回汇总错误。
9. 若关闭超时，监督器不卸载仍可能被存活 Runner 使用的资源，Atom 转为 `Failed`。这会有意保留资源而不是制造并发关闭或 use-after-close；错误会明确列出未退出的 Runner。

若没有 Runner，`RunAtom` 直接执行卸载并返回 nil。若所有 Runner 主动返回 nil，表示它们已正常完成，`RunAtom` 在全部完成后返回 nil。因调用方取消退出时返回 `context.Canceled` 或 `context.DeadlineExceeded`；因 Runner 故障退出时返回带组件名称的运行错误。Runner 错误与关闭/卸载错误同时存在时使用 `errors.Join` 全部保留。

## 8. 命令与错误模型

Data 和 Ability 统一返回：

```go
type CommandOutput struct {
    Name  string
    Value any
    Err   error
}

func (o CommandOutput) Success() bool { return o.Err == nil }
```

每个内置命令提供导出常量。TimeAbility 的网络、手工、NTP 和运行配置命令分别使用独立参数类型，不再用一个包含无关字段的万能参数结构体。未知命令使用 `ErrUnsupportedCommand`，参数类型或内容错误使用 `ErrInvalidArguments`，调用方可通过 `errors.Is` 判断。组件使用 `%w` 包装底层错误，不再折叠成诸如 `"fetch failed"` 的无上下文字符串。

核心库不打印命令执行日志。BaseData 的 logo/info、BaseAbility 的名称列表、RoleAbility 的角色和 TimeAbility 的同步信息均通过 `Value` 返回。列表按名称排序，保证结果和测试稳定。

## 9. TimeAbility

### 9.1 时间模型

TimeAbility 在每次成功同步时保存来源时间、来源名称和本地单调时钟基准。当前时间按以下方式计算：

```text
synced time + monotonic elapsed since sync
```

生产实现使用 Go `time.Time` 携带的单调时钟信息，不再通过当前系统壁钟与旧壁钟的差值推算。因此系统时间被管理员、NTP 或攻击者向前/后调整时，已同步时间不会跳变。

首次默认同步采用写锁内二次检查，避免并发 `get_time` 覆盖刚完成的网络、NTP 或手工同步。所有可变字段由同一互斥锁保护。

### 9.2 HTTP 对时

默认网络策略：

- 仅允许 `http` 和 `https`。
- 总请求超时 5 秒。
- 仅接受 2xx 状态码。
- 响应体最多 64 KiB。
- 校验初始目标和每次重定向。
- 禁用环境代理，确保实际连接目标经过本地地址策略；未来若支持代理，必须作为独立显式配置校验。
- 拒绝从 HTTPS 重定向降级到 HTTP。
- 默认拒绝 unspecified、loopback、private、link-local 和 multicast 地址。
- 自定义 DialContext 在解析后验证实际连接 IP，降低 DNS rebinding 绕过风险。
- 使用独立构造的 Transport，不继承进程可能修改过的 `http.DefaultTransport`。

边缘设备需要访问局域网或本机时间服务时，调用方必须通过显式选项启用私网范围。启用后允许 private、loopback 和 link-local 单播地址，仍拒绝 unspecified 和 multicast 地址。该开关不改变协议、超时、状态码和响应大小限制。

JSON 解析继续兼容 `dateTime` 与 `DateTime` 字段，缺少字段或时间格式非法时保留具体错误。

### 9.3 NTP 与 ticker

NTP 使用 `ntp.QueryWithOptions` 设置超时并调用响应 `Validate`。NTP 目标沿用 HTTP 对时的地址范围策略，通过自定义解析与 dialer 防止默认配置探测本机或私网；显式启用私网范围后可连接局域网 NTP。测试通过包内注入的 NTP 查询接口返回确定结果，不访问公网。

TimeAbility 提供 `configure_run` 命令，在进入 `RunAtom` 前设置 `monotonic` 或 `ticker` 模式与刷新间隔。默认 `monotonic` 模式按需计算时间，TimeAbility 的 Runner 仅等待 context；`ticker` 模式用于需要按固定粒度刷新缓存时间的场景。默认最小间隔为 1 毫秒；更小间隔返回参数错误。ticker 只在 `Run` 内创建，收到 context 后立即停止。重复启动由 Atom 状态机拒绝。

TimeAbility 含互斥锁和一次性初始化状态，只能以指针注册和使用，禁止首次使用后按值复制。

## 10. 其他内置组件

- BaseData：返回 logo 和版本信息，不直接打印。
- BaseAbility：返回排序后的 Data/Ability 名称；移除 `blocking`、`runnable` 和 `run` 命令。
- RoleAbility：为角色字段增加互斥锁；`set_role` 拒绝错误类型和空白角色。
- 所有组件的 `Check`、`Mount` 和命令参数都返回可判断、可包装的错误。

## 11. 测试策略

### 11.1 Atom 与生命周期

- nil/空名称/重复组件注册失败。
- 带类型 nil 和注册后名称变化被拒绝。
- getter 返回副本，外部修改不影响 Atom。
- 非 `Created` 状态注册失败。
- 检查失败时不挂载任何组件。
- 挂载失败时不进入运行集合，并逆序卸载已挂载组件。
- Runner 并发启动并保持运行，调用方取消后全部退出。
- Runner 错误取消兄弟 Runner，原始错误可通过 `errors.Is/As` 获取。
- Check、Mount、Runner 和 Unmount panic 被隔离并保留组件名称与堆栈。
- 不响应 context 的 Runner 触发关闭超时。
- 并发读取注册表和运行生命周期在 `go test -race` 下无数据竞争。

### 11.2 命令与内置组件

- 未知命令和错误参数类型返回对应 sentinel error。
- BaseAbility 列表结果稳定排序。
- RoleAbility 并发读写无 race，空角色不被接受。
- Mount/Check 错误不再被创建后丢弃。

### 11.3 TimeAbility

- 手工、系统、HTTP 和 NTP 同步成功及错误路径。
- 系统壁钟跳变不影响单调推算结果。
- 首次读取与外部同步并发时不覆盖外部来源。
- HTTP 超时、非 2xx、超大响应、非法 JSON、重定向和地址范围策略。
- HTTP 环境代理不能绕过地址策略，HTTPS 不能重定向降级为 HTTP。
- 默认拒绝本机/私网目标；显式配置后允许本地测试服务器。
- NTP 测试不访问公网。
- ticker 精度下限、context 取消和资源停止。

## 12. 完成标准

以下命令全部通过且工作区没有意外文件：

```bash
gofmt -l $(git ls-files '*.go')
go mod tidy -diff
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
git diff --check
git status --short
```

其中前两条必须无输出。测试不得访问公网，也不得依赖执行顺序或任意 sleep 才能稳定通过。

## 13. 文档与迁移

README 增加：

- FasterEdge 当前是库而非独立可执行程序的说明。
- `InitAtom`、注册组件、`PreRunAtom`、创建 context、调用 `RunAtom` 和取消退出的完整示例。
- 长期 Ability 实现 `Runner` 的最小示例。
- TimeAbility 默认公网策略与显式允许内网时间源的安全说明。
- 当前 API 与新 API 的迁移表，特别说明 Atom 指针、error 返回、私有注册表和移除的魔法命令。

本轮不创建示例守护进程或网络传输实现，确保实现范围集中在可测试的核心契约。
