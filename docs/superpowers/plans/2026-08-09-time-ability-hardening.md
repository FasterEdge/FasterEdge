# FasterEdge TimeAbility Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make TimeAbility monotonic, race-free, strictly validated, cancellable, and safe against unbounded or policy-bypassing HTTP/NTP time sources while explicitly supporting trusted LAN services.

**Architecture:** TimeAbility stores a synchronized wall time plus an injected monotonic counter, so later system-wall-clock changes cannot corrupt elapsed time. Network access is split into address policy, direct dialing, HTTP parsing, and NTP querying. TimeAbility implements `types.Runner` for its configured monotonic or ticker mode and uses the core supervisor delivered by the preceding lifecycle plan.

**Tech Stack:** Go 1.25.5 standard library (`context`, `encoding/json`, `errors`, `io`, `net`, `net/http`, `net/netip`, `net/url`, `sync`, `time`), `github.com/beevik/ntp v1.5.0`, `httptest`, built-in race detector.

**Design:** `docs/superpowers/specs/2026-08-09-core-hardening-design.md`

**Prerequisite:** `docs/superpowers/plans/2026-08-09-core-lifecycle-hardening.md` is complete and the worktree is clean.

---

## File map

- Replace `ability/time_ability.go`: state, options, strict commands, monotonic calculation, and Runner.
- Create `ability/time_clock.go`: production clock/ticker abstractions.
- Create `ability/time_network.go`: URL validation, address classification, DNS resolution, and direct TCP/UDP dialing.
- Create `ability/time_http.go`: bounded HTTP client and JSON time response parsing.
- Create `ability/time_ntp.go`: validated NTP query wrapper and policy-aware dialer.
- Create `ability/time_ability_test.go`: command, monotonic, race, and Runner tests.
- Create `ability/time_network_test.go`: IPv4/IPv6 scope and DNS-result tests.
- Create `ability/time_http_test.go`: local HTTP server and redirect-policy tests.
- Create `ability/time_ntp_test.go`: offline query-substitute tests.
- Modify `README.md`: public/private source policy and time-runner examples.

### Task 1: Strict time commands and a monotonic synchronization model

**Files:**
- Replace: `ability/time_ability.go`
- Create: `ability/time_clock.go`
- Create: `ability/time_ability_test.go`

- [ ] **Step 1: Write failing strict-command and monotonic-clock tests**

Use `package ability` so tests can inject an unexported fake clock without adding a public testing API. Define:

```go
type fakeTimeClock struct {
    mu        sync.Mutex
    wall      time.Time
    monotonic time.Duration
}

func (c *fakeTimeClock) Now() time.Time {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.wall
}

func (c *fakeTimeClock) Monotonic() time.Duration {
    c.mu.Lock()
    defer c.mu.Unlock()
    return c.monotonic
}

func (c *fakeTimeClock) advance(elapsed time.Duration, wallChange time.Duration) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.monotonic += elapsed
    c.wall = c.wall.Add(wallChange)
}
```

Add tests named `TestTimeAbilityRejectsWrongAndMissingCommandArguments`, `TestTimeAbilityManualSyncAndGetTime`, `TestTimeAbilityUsesMonotonicElapsedWhenWallClockJumps`, `TestTimeAbilityEnsureSyncedDoesNotOverwriteConcurrentExternalSync`, and `TestTimeAbilityLastReturnsSourceAndSynchronizedTime`.

The wall-jump test must:

1. create a fake clock at `2026-08-09T00:00:00Z` with monotonic value 0;
2. manually synchronize to `2030-01-01T00:00:00Z`;
3. advance monotonic time by 2 seconds while moving the wall clock backward 24 hours;
4. assert `get_time` returns `2030-01-01T00:00:02Z`.

For the concurrent-first-sync test, configure the fake clock so its first `Monotonic` call blocks on a channel while the second call used by manual synchronization proceeds. Complete manual synchronization, release the first call, and assert the source remains `manual`. No production test hook is added.

- [ ] **Step 2: Run the tests and verify RED**

Run:

```bash
go test ./ability -run 'TestTimeAbility(Rejects|Manual|Uses|Ensure|Last)' -count=1
```

Expected: failures because the existing implementation ignores assertion failures, follows the wall clock, prints instead of returning complete values, and has no deterministic clock seam.

- [ ] **Step 3: Add the production wall/monotonic clock abstraction**

Create `ability/time_clock.go`:

```go
package ability

import "time"

type timeClock interface {
    Now() time.Time
    Monotonic() time.Duration
}

type systemTimeClock struct {
    origin time.Time
}

func newSystemTimeClock() *systemTimeClock {
    return &systemTimeClock{origin: time.Now()}
}

func (c *systemTimeClock) Now() time.Time {
    return time.Now()
}

func (c *systemTimeClock) Monotonic() time.Duration {
    return time.Since(c.origin)
}

```

The origin's monotonic reading is process-local and is never serialized. Tests supply their own clock.

- [ ] **Step 4: Replace TimeAbility state and strict local commands**

Define these exported commands and values in `ability/time_ability.go`:

```go
const (
    TimeCommandSyncNetwork  = "sync_net"
    TimeCommandSyncManual   = "sync_manual"
    TimeCommandSyncSystem   = "sync_system"
    TimeCommandSyncNTP      = "sync_ntp"
    TimeCommandLastSync     = "last"
    TimeCommandGetTime      = "get_time"
)

type TimeSyncNetworkArgs struct {
    URL string
}

type TimeSyncManualArgs struct {
    Value string
}

type TimeSyncNTPArgs struct {
    Address string
}

type TimeAbilityOutput struct {
    Source string
    Time   time.Time
}
```

Use a zero-value-safe `TimeAbility` with:

```go
type TimeAbility struct {
    initOnce sync.Once
    mu       sync.RWMutex

    clock      timeClock
    lastSource string
    lastSynced time.Time
    baseMono   time.Duration

    config timeAbilityConfig
}

type timeAbilityConfig struct {
    httpTimeout       time.Duration
    ntpTimeout        time.Duration
    maxResponseBytes  int64
    minimumTick       time.Duration
    allowPrivate      bool
    defaultNetworkURL string
    defaultNTPServer  string
}

var _ types.Ability = (*TimeAbility)(nil)
```

`ensureDefaults` must install the default clock and scalar config only when the corresponding field is zero, preserving test injection. The NTP query object is introduced under its own RED tests in Task 4. `setSync` obtains `clock.Monotonic()` before taking `mu`, then atomically updates source, synchronized time, and base counter.

`currentTime` computes:

```go
elapsed := clock.Monotonic() - baseMono
return lastSynced.Add(elapsed)
```

Reject a negative elapsed value as an internal clock error rather than returning time that moves backward.

Strict command rules:

- `sync_manual` requires a `TimeSyncManualArgs` value with a non-empty RFC3339 value;
- `sync_system`, `last`, and `get_time` require nil arguments;
- `sync_net` accepts nil for the default or a `TimeSyncNetworkArgs` value;
- `sync_ntp` accepts nil for the default or a `TimeSyncNTPArgs` value;
- wrong types and invalid content wrap `types.ErrInvalidArguments`;
- unknown commands wrap `types.ErrUnsupportedCommand`;
- command methods return values and errors without printing.

Every successful sync command returns `TimeAbilityOutput` with the new source and synchronized time. `last` returns the exact last synchronized value; `get_time` returns the current monotonic/ticker-derived value with the same source.

Implement `ensureSynced` using a read check followed by a write-lock second check. In the race regression test, make the fake clock block only its first `Monotonic` call, allow the manual sync's second call to proceed, then release the first call. This creates the required interleaving without a production test hook or a sleep.

Keep the existing network and NTP helper bodies temporarily behind the new strict argument parsing so this task compiles; do not invoke them from Task 1 tests. Tasks 3 and 4 replace those helpers under dedicated failing tests.

- [ ] **Step 5: Verify GREEN and race safety**

Run:

```bash
gofmt -w ability/time_ability.go ability/time_clock.go ability/time_ability_test.go
go test ./ability -run 'TestTimeAbility(Rejects|Manual|Uses|Ensure|Last)' -count=20
go test -race ./ability -run 'TestTimeAbility(Rejects|Manual|Uses|Ensure|Last)' -count=1
```

Expected: all commands exit 0; the tests do not access the network.

- [ ] **Step 6: Commit monotonic synchronization**

```bash
git add ability/time_ability.go ability/time_clock.go ability/time_ability_test.go
git commit -m "fix: make time synchronization monotonic"
```

### Task 2: Classify addresses and dial only validated endpoints

**Files:**
- Create: `ability/time_network.go`
- Create: `ability/time_network_test.go`
- Modify: `ability/time_ability.go`

- [ ] **Step 1: Write failing public/private address-policy tests**

Create table-driven tests covering:

```text
public allowed:       1.1.1.1, 8.8.8.8, 2606:4700:4700::1111
default rejected:     0.0.0.0, 127.0.0.1, 10.0.0.1, 100.64.0.1,
                      169.254.169.254, 172.16.0.1, 192.168.1.1,
                      224.0.0.1, ::, ::1, fc00::1, fe80::1, ff02::1
private mode allowed: 127.0.0.1, 10.0.0.1, 169.254.1.1, 192.168.1.1,
                      ::1, fc00::1, fe80::1
always rejected:      unspecified and multicast addresses
```

Also test:

- IPv4-mapped IPv6 addresses are unwrapped before classification;
- a DNS result containing one public and one disallowed address rejects the whole target;
- a hostname that resolves public during URL preflight but private during actual dial is rejected before the base dial function runs;
- the dialer connects to the validated numeric IP rather than resolving the original hostname again;
- malformed host/port pairs return `ErrInvalidArguments`;
- URL validation rejects userinfo, empty host, non-HTTP schemes, and fragments.
- `NewTimeAbility(nil)` and every non-positive timeout/size option return an error wrapping `types.ErrInvalidArguments`.

Use fake resolver and dial functions that record inputs; do not use real DNS.

Assert disallowed targets wrap an exported `ErrTimeAddressDisallowed`; malformed URL/host/port inputs wrap `types.ErrInvalidArguments`.

- [ ] **Step 2: Run address tests and verify RED**

Run:

```bash
go test ./ability -run 'Test(Address|Resolve|Dial|TimeURL)' -count=1
```

Expected: build failure because the address policy does not exist.

- [ ] **Step 3: Implement explicit address scopes and resolver interfaces**

Create `ability/time_network.go` with:

```go
var ErrTimeAddressDisallowed = errors.New("time source address is disallowed")

type ipResolver interface {
    LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type addressPolicy struct {
    allowPrivate bool
    resolver     ipResolver
    tcpDial      func(context.Context, string, string) (net.Conn, error)
    udpDial      func(string, *net.UDPAddr, *net.UDPAddr) (net.Conn, error)
}

func (p addressPolicy) validateIP(net.IP) error
func (p addressPolicy) resolve(context.Context, string) ([]net.IPAddr, error)
func (p addressPolicy) dialContext(*net.Dialer) func(context.Context, string, string) (net.Conn, error)
func (p addressPolicy) dialUDP(time.Duration) func(string, string) (net.Conn, error)
func validateTimeURL(string) (*url.URL, error)
```

Convert each IP through `netip.AddrFromSlice` and `Unmap`. Always reject unspecified and multicast. In public mode reject loopback, private, link-local, carrier-grade NAT (`100.64.0.0/10`), documentation, benchmark, and other special-use prefixes. In explicit private mode allow loopback/private/link-local unicast while still rejecting unspecified, multicast, and invalid addresses.

When DNS returns multiple IPs, validate every IP before dialing any of them. Resolve and validate again inside each actual HTTP/NTP dial path, then pass only numeric addresses to `tcpDial` or `udpDial`. Dial validated numeric addresses in resolver order and aggregate connection failures with `errors.Join`. This prevents mixed answers and public-then-private DNS rebinding from reaching the base dialer. Production defaults use `net.Dialer.DialContext` and `net.DialUDP`; tests replace both functions.

After the address-policy RED tests exist, add `networkPolicy addressPolicy` to `TimeAbility`. Lazy initialization fills only nil resolver/dial functions and copies `config.allowPrivate`; package tests may inject a complete policy before first use.

- [ ] **Step 4: Add TimeAbility network options**

Use:

```go
type TimeOption func(*timeAbilityConfig) error

func NewTimeAbility(options ...TimeOption) (*TimeAbility, error)
func WithPrivateNetworkTimeSources() TimeOption
func WithHTTPTimeout(time.Duration) TimeOption
func WithNTPTimeout(time.Duration) TimeOption
func WithMaxResponseBytes(int64) TimeOption
func WithMinimumTickInterval(time.Duration) TimeOption
```

Defaults:

```go
httpTimeout:       5 * time.Second
ntpTimeout:        5 * time.Second
maxResponseBytes:  64 << 10
minimumTick:       time.Millisecond
allowPrivate:      false
defaultNetworkURL: "https://timeapi.io/api/Time/current/zone?timeZone=Asia/Shanghai"
defaultNTPServer:  "pool.ntp.org"
```

Every duration and byte limit option rejects non-positive values with `types.ErrInvalidArguments`. The constructor applies defaults first, then options, and installs the same config used by lazy zero-value initialization.

- [ ] **Step 5: Verify GREEN**

Run:

```bash
gofmt -w ability/time_network.go ability/time_network_test.go ability/time_ability.go
go test ./ability -run 'Test(Address|Resolve|Dial|TimeURL|TimeOption)' -count=1
go test -race ./ability -run 'Test(Address|Resolve|Dial|TimeURL|TimeOption)' -count=1
```

Expected: all commands exit 0 with no DNS or external connections.

- [ ] **Step 6: Commit address policy**

```bash
git add ability/time_network.go ability/time_network_test.go ability/time_ability.go
git commit -m "feat: enforce time source address policy"
```

### Task 3: Bound and validate HTTP time synchronization

**Files:**
- Create: `ability/time_http.go`
- Create: `ability/time_http_test.go`
- Modify: `ability/time_ability.go`

- [ ] **Step 1: Write failing HTTP behavior tests**

Use `httptest.NewServer` only with `WithPrivateNetworkTimeSources()`. Add tests named `TestHTTPTimeSyncSuccess`, `TestHTTPTimeSyncRejectsNon2xx`, `TestHTTPTimeSyncRejectsBodyOverLimit`, `TestHTTPTimeSyncRejectsMissingOrInvalidDateTime`, `TestHTTPTimeSyncTimesOut`, `TestHTTPTimeSyncDoesNotHoldStateLock`, `TestHTTPClientIgnoresEnvironmentProxy`, `TestHTTPRedirectRevalidatesEveryTarget`, `TestHTTPRedirectRejectsHTTPSDowngrade`, and `TestHTTPTimeSyncDefaultPolicyRejectsLoopback`.

Assert body overflow wraps exported `ErrTimeResponseTooLarge` and HTTPS downgrade wraps exported `ErrHTTPSDowngrade`.

The timeout handler waits on `r.Context().Done()`; the test does not sleep. Set `HTTP_PROXY` and `HTTPS_PROXY` to an unreachable local address with `t.Setenv` and assert the explicitly allowed local server still succeeds, proving the transport does not use environment proxies.

For the lock test, block the HTTP handler on a channel, issue `sync_net` in a goroutine, then call `last` and `get_time` synchronously before releasing the handler. Both local commands must return while the request remains blocked.

Test the redirect callback directly for HTTPS-to-HTTP downgrade. For redirect address checks, redirect an allowed local test server to a target rejected by a fake resolver before any target connection occurs.

- [ ] **Step 2: Run HTTP tests and verify RED**

Run:

```bash
go test ./ability -run 'TestHTTP' -count=1
```

Expected: failures because HTTP requests still use the global client, do not enforce status/size/address policy, and collapse errors.

- [ ] **Step 3: Build a direct, policy-aware HTTP client**

Create `ability/time_http.go`:

```go
var (
    ErrHTTPSDowngrade     = errors.New("HTTPS redirect downgrade is disallowed")
    ErrTimeResponseTooLarge = errors.New("time response exceeds configured limit")
)
```

- construct a new private `http.Transport` rather than cloning process-mutable `http.DefaultTransport`;
- leave `Proxy = nil` so environment proxies cannot bypass target validation;
- set `DialContext` to `addressPolicy.dialContext`, `ForceAttemptHTTP2 = true`, `TLSHandshakeTimeout = 10*time.Second`, `IdleConnTimeout = 90*time.Second`, `ExpectContinueTimeout = time.Second`, and `ResponseHeaderTimeout` from config;
- set client `Timeout` from config;
- set `CheckRedirect` to enforce the normal redirect limit, validate every URL/address, and reject HTTPS-to-HTTP downgrade;
- never mutate `http.DefaultTransport` or `http.DefaultClient`.

Implement:

```go
func (t *TimeAbility) fetchNetworkTime(ctx context.Context, rawURL string) (time.Time, error)
```

It must:

1. validate the URL before constructing the request;
2. create `http.NewRequestWithContext`;
3. require a 2xx response;
4. read through `io.LimitReader(body, maxResponseBytes+1)`;
5. reject a body whose length exceeds the configured limit;
6. decode `dateTime` or `DateTime` and parse RFC3339Nano;
7. wrap request, status, read, JSON, missing-field, and parse errors without losing their causes.

The command layer calls this method with a timeout context and passes the exact error through `CommandOutput.Err`.

- [ ] **Step 4: Verify GREEN and repeat timeout-sensitive tests**

Run:

```bash
gofmt -w ability/time_http.go ability/time_http_test.go ability/time_ability.go
go test ./ability -run 'TestHTTP' -count=20
go test -race ./ability -run 'TestHTTP' -count=1
```

Expected: all commands exit 0. No test contacts a host other than its in-process test server.

- [ ] **Step 5: Commit HTTP hardening**

```bash
git add ability/time_http.go ability/time_http_test.go ability/time_ability.go
git commit -m "fix: harden HTTP time synchronization"
```

### Task 4: Validate NTP responses without live-network tests

**Files:**
- Create: `ability/time_ntp.go`
- Create: `ability/time_ntp_test.go`
- Modify: `ability/time_ability.go`

- [ ] **Step 1: Write failing NTP tests using a query substitute**

Define an unexported interface:

```go
type ntpQueryClient interface {
    QueryWithOptions(string, ntp.QueryOptions) (*ntp.Response, error)
}
```

Add tests that inject a fake implementation and assert:

- default server, configured timeout, policy dialer, and injected-clock `GetSystemTime` reach the query;
- a valid response is validated and its `ClockOffset` is added to the injected clock's wall time;
- query errors and `Response.Validate` errors remain reachable through `errors.Is`;
- a `(nil, nil)` query result returns an error rather than panicking;
- a custom LAN server is rejected by default and permitted only with the private-network option;
- the generated `QueryOptions.Dialer` uses the same address policy and configured timeout;
- nil or wrongly typed command arguments fail without invoking the query.
- a query substitute blocked on a channel does not prevent concurrent `last` or `get_time` commands, proving no network operation occurs under `TimeAbility.mu`.

Use this exact valid fixture and vary one field per invalid-response test:

```go
now := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
response := &ntp.Response{
    ClockOffset:   250 * time.Millisecond,
    Stratum:       2,
    Leap:          ntp.LeapNoWarning,
    Time:          now,
    ReferenceTime: now.Add(-time.Second),
}
```

Set `Stratum = 0` to assert `errors.Is(err, ntp.ErrKissOfDeath)`. Do not add a validation seam and do not call `pool.ntp.org`.

- [ ] **Step 2: Run NTP tests and verify RED**

Run:

```bash
go test ./ability -run 'TestNTP' -count=1
```

Expected: failures because `ntp.Time` is called directly and no query or dialer seam exists.

- [ ] **Step 3: Implement the NTP adapter and synchronization**

Add `ntpQuery ntpQueryClient` to `TimeAbility`; lazy initialization installs the production adapter only when tests did not inject one. Create `ability/time_ntp.go` with a production adapter whose `QueryWithOptions` method calls `ntp.QueryWithOptions`. Normalize a host without a port to port 123, resolve and validate the target before invoking the query substitute, and build options containing:

```go
ntp.QueryOptions{
    Timeout:       config.ntpTimeout,
    Dialer:        policy.dialUDP(config.ntpTimeout),
    GetSystemTime: clock.Now,
}
```

After querying:

1. return query errors unchanged under a descriptive wrapper;
2. reject a nil response;
3. call `response.Validate()`;
4. compute synchronized time as `clock.Now().Add(response.ClockOffset)`;
5. store source as `"ntp:" + server` only after every step succeeds.

The policy-aware UDP dialer resolves and validates every address, dials a numeric `net.UDPAddr`, and sets a connection deadline. It preserves an IPv6 zone for explicitly allowed link-local sources.

- [ ] **Step 4: Verify GREEN and prove tests are offline**

Run:

```bash
gofmt -w ability/time_ntp.go ability/time_ntp_test.go ability/time_ability.go
go test ./ability -run 'TestNTP' -count=20
go test -race ./ability -run 'TestNTP' -count=1
```

Expected: all commands exit 0. The fake query's call counter proves each test uses the substitute.

- [ ] **Step 5: Commit NTP hardening**

```bash
git add ability/time_ntp.go ability/time_ntp_test.go ability/time_ability.go
git commit -m "fix: validate NTP time synchronization"
```

### Task 5: Run TimeAbility continuously under context

**Files:**
- Modify: `ability/time_ability.go`
- Modify: `ability/time_clock.go`
- Modify: `ability/time_ability_test.go`
- Modify: `atom_test.go`

- [ ] **Step 1: Write failing configuration and Runner tests**

Create a controllable fake ticker:

```go
type fakeTimeTicker struct {
    ch      chan time.Time
    stopped chan struct{}
    once    sync.Once
}

func (t *fakeTimeTicker) Chan() <-chan time.Time { return t.ch }
func (t *fakeTimeTicker) Stop() {
    t.once.Do(func() { close(t.stopped) })
}
```

Add tests named `TestTimeAbilityConfigureRunRejectsUnknownModeAndTooSmallInterval`, `TestTimeAbilityMonotonicRunnerBlocksUntilContextCancellation`, `TestTimeAbilityTickerRunnerUpdatesCachedTime`, `TestTimeAbilityRunnerStopsTickerOnCancellation`, `TestTimeAbilityRejectsConcurrentDirectRun`, and `TestRunAtomSupervisesTimeAbility`.

Use start/tick/stopped channels, not sleep. The root integration test registers a real zero-value TimeAbility in monotonic mode, calls `PreRunAtom`, starts `RunAtom`, condition-waits with `runtime.Gosched` and a test deadline until `atom.State() == types.AtomRunning`, cancels the context, and asserts `errors.Is(err, context.Canceled)`. Package-level Runner tests use the fake clock/ticker.

- [ ] **Step 2: Run Runner tests and verify RED**

Run:

```bash
go test ./ability . -run 'TestTimeAbility.*Run|TestRunAtomSupervisesTimeAbility' -count=1
```

Expected: failures because TimeAbility does not implement the intended `types.Runner` contract or enforce run configuration.

- [ ] **Step 3: Implement configure_run and Runner**

Add the configuration API to `time_ability.go` and the ticker types to `time_clock.go` only after the RED tests exist:

```go
const TimeCommandConfigureRun = "configure_run"

type TimeRunMode string

const (
    TimeRunModeMonotonic TimeRunMode = "monotonic"
    TimeRunModeTicker    TimeRunMode = "ticker"
)

type TimeConfigureRunArgs struct {
    Mode     TimeRunMode
    Interval time.Duration
}

type timeTicker interface {
    Chan() <-chan time.Time
    Stop()
}

type realTimeTicker struct {
    *time.Ticker
}

func (t realTimeTicker) Chan() <-chan time.Time { return t.C }
```

Add `current time.Time`, `runMode TimeRunMode`, `interval time.Duration`, `running bool`, and `newTicker func(time.Duration) timeTicker` under `TimeAbility.mu`. Lazy initialization sets `newTicker` to `func(interval time.Duration) timeTicker { return realTimeTicker{Ticker: time.NewTicker(interval)} }`; tests inject their fake factory. Initialize the default mode to `TimeRunModeMonotonic` and the default ticker interval to `minimumTick`.

Add `var _ types.Runner = (*TimeAbility)(nil)` beside the existing Ability assertion. Because TimeAbility contains `sync.Once` and `sync.RWMutex`, documentation and examples must only register `*TimeAbility`; never copy it after first use.

Parse `TimeConfigureRunArgs.Mode` case-insensitively:

- empty or `monotonic` selects `TimeRunModeMonotonic` and accepts only an exact zero interval;
- `ticker` uses `Interval == 0` as the configured minimum/default interval;
- reject negative intervals and positive values below `minimumTick`;
- reject configuration while `running` is true; do not couple command validation to Atom state because the core supervisor already owns lifecycle state.

Implement:

```go
func (t *TimeAbility) Run(ctx context.Context, atom *types.Atom) error
```

Behavior:

- reject nil context and nil Atom;
- atomically reject a concurrent direct Run and set `running = true` until return;
- call `ensureSynced`;
- monotonic mode blocks on `ctx.Done()` and returns nil so the supervisor owns the cancellation error;
- ticker mode creates exactly one ticker, defers `Stop`, updates cached current time on each tick using monotonic elapsed, and exits nil on cancellation;
- unexpected negative monotonic elapsed returns an error, causing the Atom supervisor to cancel sibling Runners.

- [ ] **Step 4: Verify GREEN and stress cancellation**

Run:

```bash
gofmt -w ability/time_ability.go ability/time_ability_test.go atom_test.go
go test ./ability . -run 'TestTimeAbility.*Run|TestRunAtomSupervisesTimeAbility' -count=50
go test -race ./ability . -run 'TestTimeAbility.*Run|TestRunAtomSupervisesTimeAbility' -count=1
```

Expected: all commands exit 0 with no leaked ticker reported by test channels.

- [ ] **Step 5: Commit context-controlled TimeAbility execution**

```bash
git add ability/time_ability.go ability/time_clock.go ability/time_ability_test.go atom_test.go
git commit -m "feat: run TimeAbility under context"
```

### Task 6: Document secure time sources and run the complete gate

**Files:**
- Modify: `README.md`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add TimeAbility examples and security behavior to README**

Include compilable examples for:

- default public HTTP/NTP source policy;
- constructing with `NewTimeAbility(WithPrivateNetworkTimeSources())` for a trusted LAN service;
- manual synchronization;
- configuring monotonic and ticker modes before `RunAtom`;
- cancellation and error handling.

State explicitly that enabling private sources permits loopback/private/link-local destinations but does not disable protocol, timeout, redirect, status, or response-size checks.

- [ ] **Step 2: Normalize files and dependency metadata**

Run:

```bash
gofmt -w $(git ls-files '*.go')
go mod tidy
```

Inspect `go.mod` and `go.sum`. The only direct non-standard dependency remains `github.com/beevik/ntp`.

- [ ] **Step 3: Run focused repeatability checks**

Run:

```bash
go test ./ability -run 'Test(Time|HTTP|NTP|Address)' -count=50
go test -race ./ability -run 'Test(Time|HTTP|NTP|Address)' -count=1
```

Expected: both commands exit 0 without external DNS, HTTP, or NTP access.

- [ ] **Step 4: Run the repository completion gate**

Run:

```bash
gofmt -l $(git ls-files '*.go')
go mod tidy -diff
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
git diff --check
```

Expected: every command exits 0; `gofmt -l` and `go mod tidy -diff` print nothing.

- [ ] **Step 5: Commit documentation and final dependency state**

```bash
git add README.md go.mod go.sum
git commit -m "docs: document secure time sources"
```

- [ ] **Step 6: Confirm the implementation series is clean**

Run:

```bash
git status --short
git log --oneline --decorate -11
```

Expected: status is empty and the core plus TimeAbility implementation commits appear after the design and plan commits.
