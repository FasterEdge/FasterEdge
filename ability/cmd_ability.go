// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FasterEdge/FasterEdge/types"
)

const (
	CmdCommandRun          = "run"
	CmdCommandStart        = "start"
	CmdCommandWait         = "wait"
	CmdCommandKill         = "kill"
	CmdCommandList         = "list"
	CmdCommandSetAllowlist = "set_allowlist"
	CmdCommandGetAllowlist = "get_allowlist"
	CmdCommandSetAllowAny  = "set_allowlist_any"
	CmdCommandClearJobs    = "clear_finished"
	CmdCommandGetJob       = "get_job"
)

const (
	cmdDefaultMaxOutputBytes = 1 << 20 // 1 MiB
	cmdDefaultMaxRunTimeout  = 30 * time.Second
	cmdDefaultMaxConcurrent  = 16
)

// CmdAllowlistEntry 描述一条允许执行的命令规则。
// Name 精确匹配可执行文件名(不含路径);ArgsPrefix 是按顺序必须匹配的前缀参数,空表示不限制。
// 例如: {"Name":"sh","ArgsPrefix":[],"MaxArgs":0} 允许 sh 任意参数。
//
//	{"Name":"ls","ArgsPrefix":[],"MaxArgs":2}    允许 ls 带最多 2 个参数。
//
// 授权语义为 fail-closed: allowlist 为空时拒绝一切执行(ErrAllowlistEmpty);
// 需要放行任意命令必须显式 opt-in(SetAllowAny / set_allowlist_any)。
type CmdAllowlistEntry struct {
	Name       string
	ArgsPrefix []string
	MaxArgs    int // 0 表示不限制
}

// CmdRunArgs 是 run/start 命令的通用参数。
type CmdRunArgs struct {
	Name    string
	Args    []string
	Timeout time.Duration
	// Env 原样透传给子进程环境(空时继承父进程全部环境; 非空时整体替换)。
	// 注意: 不做"白名单过滤"——调用方(认证边界内)对其内容负责。
	Env []string
}

// CmdWaitArgs 是 wait 命令的参数。
type CmdWaitArgs struct {
	JobID string
	Wait  time.Duration
}

// CmdKillArgs 是 kill 命令的参数。
type CmdKillArgs struct {
	JobID string
}

// CmdJobIDArg 是 get_job / clear_finished 等命令用的参数。
type CmdJobIDArg struct {
	JobID string
}

// CmdSetAllowlistArgs 是 set_allowlist 命令的参数。
type CmdSetAllowlistArgs struct {
	Entries []CmdAllowlistEntry
}

// CmdSetAllowAnyArgs 是 set_allowlist_any 命令的参数(显式 opt-in)。
// Any=false(默认)时恢复 fail-closed 语义: 清空 allowlist 并拒绝一切执行;
// Any=true 时允许执行任意命令(等价于一条 {"Name":"*", MaxArgs:0} 全放行规则)。
type CmdSetAllowAnyArgs struct {
	Any bool
}

// CmdResult 是 run / wait 命令成功执行后的返回结构。
type CmdResult struct {
	JobID     string
	Name      string
	Args      []string
	ExitCode  int
	Stdout    string
	Stderr    string
	Started   time.Time
	Duration  time.Duration
	Truncated bool
}

// CmdJob 是异步任务的运行时记录,只通过 list / get_job 暴露给外部。
type CmdJob struct {
	JobID     string
	Name      string
	Args      []string
	Started   time.Time
	Done      bool
	ExitCode  int
	Stdout    string
	Stderr    string
	Truncated bool
	cancel    context.CancelFunc
	done      chan struct{}
}

// cmdClosingError 表示能力已被 Unmount 关闭后再次接收新命令。
var cmdClosingError = errors.New("CmdAbility is closing")

// ErrAllowlistEmpty 表示 allowlist 为空且调用方未显式 opt-in。
// 授权模型是 fail-closed: 空 allowlist 拒绝一切执行(而不是"不限制"),
// 需要全放行的使用者必须显式调用 SetAllowAny(true) 主动打开。
// 与 ErrInvalidArguments 一同包裹, 便于调用方既能机器识别授权拒绝,
// 又保持"参数/授权校验失败"这一既有哨兵语义。
var ErrAllowlistEmpty = errors.New("allowlist is empty: fail-closed denies execution")

// CmdAllowlistEntry 的 Name 匹配通配符: allowAny 模式下它匹配任意可执行文件。
const cmdAllowAnyName = "*"

// CmdAbility 提供受 allowlist 约束的命令执行能力,支持同步运行与异步任务。
//
// 授权语义(fail-closed): 默认 allowlist 为空, run/start 一律以
// ErrAllowlistEmpty 拒绝; 使用者必须显式授权——要么 set_allowlist 列出具体
// 命令, 要么 set_allowlist_any 主动打开"全放行"。空 allowlist 不再等价于
// "不限制"(旧实现的 fail-open 洞)。
type CmdAbility struct {
	mu sync.RWMutex

	closing    bool
	allowlist  map[string]CmdAllowlistEntry
	allowAny   bool // 显式 opt-in: true 时 allowlist 仅含一条 * 规则
	jobs       map[string]*CmdJob
	jobSeq     atomic.Uint64
	maxOutput  int64
	maxRunTo   time.Duration
	maxConc    int
	running    atomic.Int32
	cancelGate chan struct{}
}

func NewCmdAbility() *CmdAbility {
	return &CmdAbility{
		allowlist:  make(map[string]CmdAllowlistEntry),
		jobs:       make(map[string]*CmdJob),
		maxOutput:  cmdDefaultMaxOutputBytes,
		maxRunTo:   cmdDefaultMaxRunTimeout,
		maxConc:    cmdDefaultMaxConcurrent,
		cancelGate: make(chan struct{}),
	}
}

func (c *CmdAbility) GetName() string { return "CmdAbility" }

func (c *CmdAbility) Describe() string {
	return "CmdAbility提供受 allowlist 约束的系统命令执行能力,支持同步运行与异步任务,限制最大输出与并发数。授权语义为 fail-closed: allowlist 为空时拒绝一切执行,需显式 set_allowlist_any 打开全放行。"
}

func (c *CmdAbility) Check(atom *types.Atom) error {
	if atom == nil {
		return types.ErrMissingDependency
	}
	if _, ok := atom.Data("BaseData"); !ok {
		return types.ErrMissingDependency
	}
	return nil
}

func (c *CmdAbility) Mount(atom *types.Atom) error { return c.Check(atom) }

// beginShutdown 把 ability 标记为 closing 并关闭 cancelGate。返回是否成功。
// 若已 closing,返回 false。
func (c *CmdAbility) beginShutdown() bool {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return false
	}
	c.closing = true
	select {
	case <-c.cancelGate:
		cancelGate := c.cancelGate
		c.cancelGate = nil
		c.mu.Unlock()
		close(cancelGate)
	default:
		c.mu.Unlock()
	}
	return true
}

// Unmount cancels every active asynchronous job and waits for completion or
// context cancellation. Job state is always read and written under c.mu.
// All job fields are only mutated under c.mu to make `done` / `cancel` reads
// race-free.
func (c *CmdAbility) Unmount(ctx context.Context, _ *types.Atom) error {
	if ctx == nil {
		return types.ErrNilContext
	}
	if !c.beginShutdown() {
		return nil
	}
	c.mu.RLock()
	jobs := make([]*CmdJob, 0, len(c.jobs))
	for _, job := range c.jobs {
		jobs = append(jobs, job)
	}
	c.mu.RUnlock()
	for _, job := range jobs {
		c.mu.RLock()
		done := job.Done
		cancel := job.cancel
		wait := job.done
		c.mu.RUnlock()
		if !done && cancel != nil {
			cancel()
		}
		if wait != nil {
			select {
			case <-wait:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

// SetAllowlist 直接覆盖 allowlist(供测试或初始化使用)。
// 传空切片即清空白名单, 回到 fail-closed(拒绝一切执行);
// 需要放行任意命令必须显式调用 SetAllowAny(true)。
func (c *CmdAbility) SetAllowlist(entries []CmdAllowlistEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowAny = false
	c.allowlist = make(map[string]CmdAllowlistEntry, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		e.Name = name
		// 深拷贝 ArgsPrefix: 旧实现与调用方切片共享底层数组——调用方 set 后
		// 修改切片元素会无锁改写白名单(数据竞争 + 可绕过参数前缀约束)。
		e.ArgsPrefix = append([]string(nil), e.ArgsPrefix...)
		c.allowlist[name] = e
	}
}

// SetAllowAny 显式打开/关闭"允许任意命令"。
// true 时 allowlist 装入唯一的通配规则 {"Name":"*", MaxArgs:0}, run/start 不再
// 因白名单为空而拒绝; false 时清空 allowlist, 回到 fail-closed。
// 这是一个显式的 opt-in: 缺省(空 allowlist)永不放行任意命令。
func (c *CmdAbility) SetAllowAny(any_ bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.allowAny = any_
	if any_ {
		if _, ok := c.allowlist[cmdAllowAnyName]; !ok {
			c.allowlist = map[string]CmdAllowlistEntry{cmdAllowAnyName: {Name: cmdAllowAnyName}}
		}
		return
	}
	c.allowlist = make(map[string]CmdAllowlistEntry)
}

// AllowAny 返回当前是否处于显式"允许任意命令"模式。
func (c *CmdAbility) AllowAny() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.allowAny
}

// rejectIfClosing 检查 ability 是否处于 closing 状态,用于拒绝新任务。
func (c *CmdAbility) rejectIfClosing(act string) *types.CommandOutput {
	c.mu.RLock()
	closing := c.closing
	c.mu.RUnlock()
	if closing {
		out := types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, cmdClosingError)}
		return &out
	}
	return nil
}

func (c *CmdAbility) Command(atom *types.Atom, act string, args any) types.CommandOutput {
	if err := c.Check(atom); err != nil {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, err)}
	}
	switch act {
	case CmdCommandSetAllowlist:
		typed, ok := args.(CmdSetAllowlistArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		c.SetAllowlist(typed.Entries)
		return types.CommandOutput{Name: act, Value: c.allowlistSnapshot()}
	case CmdCommandGetAllowlist:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: c.allowlistSnapshot()}
	case CmdCommandSetAllowAny:
		typed, ok := args.(CmdSetAllowAnyArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		c.SetAllowAny(typed.Any)
		return types.CommandOutput{Name: act, Value: c.AllowAny()}
	case CmdCommandRun:
		typed, ok := args.(CmdRunArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if out := c.rejectIfClosing(act); out != nil {
			return *out
		}
		return c.runSync(typed)
	case CmdCommandStart:
		typed, ok := args.(CmdRunArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		if out := c.rejectIfClosing(act); out != nil {
			return *out
		}
		return c.startAsync(typed)
	case CmdCommandWait:
		typed, ok := args.(CmdWaitArgs)
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return c.waitJob(typed)
	case CmdCommandKill:
		typed, ok := args.(CmdKillArgs)
		if !ok || strings.TrimSpace(typed.JobID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		found, killed := c.killJob(strings.TrimSpace(typed.JobID))
		if !found {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: job %q not found: %w", act, typed.JobID, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: killed}
	case CmdCommandList:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: c.listJobs()}
	case CmdCommandGetJob:
		typed, ok := args.(CmdJobIDArg)
		if !ok || strings.TrimSpace(typed.JobID) == "" {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		job, ok := c.snapshotJob(strings.TrimSpace(typed.JobID))
		if !ok {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: job %q not found: %w", act, typed.JobID, types.ErrInvalidArguments)}
		}
		return types.CommandOutput{Name: act, Value: job}
	case CmdCommandClearJobs:
		if args != nil {
			return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w", act, types.ErrInvalidArguments)}
		}
		n := c.clearFinished()
		return types.CommandOutput{Name: act, Value: n}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("command %s: %w", act, types.ErrUnsupportedCommand)}
}

func (c *CmdAbility) allowlistSnapshot() []CmdAllowlistEntry {
	c.mu.RLock()
	out := make([]CmdAllowlistEntry, 0, len(c.allowlist))
	for _, e := range c.allowlist {
		// 深拷贝 ArgsPrefix(与 SetAllowlist 对称): 返回值不得与内部 map
		// 共享底层数组, 否则调用方改写会与并发 matchAllowlist 数据竞争。
		e.ArgsPrefix = append([]string(nil), e.ArgsPrefix...)
		out = append(out, e)
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// 显式全放行时 allowlist 里装着唯一的 "*" 通配规则, 因此 get_allowlist 的
	// 返回值本身就能区分"空 = fail-closed 拒绝一切"与"已 opt-in 全放行"
	// ([] 与 [{"Name":"*"}]), 调用方无需额外查询。
	return out
}

// matchAllowlist 返回 (entry, ok, emptyAllowlist)。ok=false 时:
// emptyAllowlist=true 表示白名单为空(fail-closed 默认拒绝), false 表示命令不在白名单内。
func (c *CmdAbility) matchAllowlist(name string, args []string) (CmdAllowlistEntry, bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// fail-closed: 空 allowlist 一律拒绝(不再解释为"不限制"), 需显式 opt-in。
	if len(c.allowlist) == 0 {
		return CmdAllowlistEntry{}, false, true
	}
	entry, ok := c.allowlist[name]
	if !ok {
		if c.allowAny {
			entry, ok = c.allowlist[cmdAllowAnyName]
		}
		if !ok {
			return CmdAllowlistEntry{}, false, false
		}
	}
	if len(args) < len(entry.ArgsPrefix) {
		return CmdAllowlistEntry{}, false, false
	}
	for i, p := range entry.ArgsPrefix {
		if args[i] != p {
			return CmdAllowlistEntry{}, false, false
		}
	}
	if entry.MaxArgs > 0 && len(args) > entry.MaxArgs {
		return CmdAllowlistEntry{}, false, false
	}
	return entry, true, false
}

// denyCmd 把 run/start 的授权拒绝统一翻译为明确错误: 空 allowlist 走
// ErrAllowlistEmpty(含 fail-closed 提示与 opt-in 指引), 其余按 not allowed。
// 两种拒绝都保留 ErrInvalidArguments 哨兵: 授权/参数校验失败本是同一个对外
// 语义(既有调用方与测试按它判断), ErrAllowlistEmpty 只是让"白名单为空"
// 这一个具体原因可被机器识别。
func (c *CmdAbility) denyCmd(act, name string, empty bool) types.CommandOutput {
	if empty {
		return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %w (use %s or %s to grant execution): %w", act, ErrAllowlistEmpty, CmdCommandSetAllowlist, CmdCommandSetAllowAny, types.ErrInvalidArguments)}
	}
	return types.CommandOutput{Name: act, Err: fmt.Errorf("%s: %q not allowed: %w", act, name, types.ErrInvalidArguments)}
}

func (c *CmdAbility) runSync(a CmdRunArgs) types.CommandOutput {
	if strings.TrimSpace(a.Name) == "" {
		return types.CommandOutput{Name: CmdCommandRun, Err: fmt.Errorf("%s: empty name: %w", CmdCommandRun, types.ErrInvalidArguments)}
	}
	if _, ok, empty := c.matchAllowlist(a.Name, a.Args); !ok {
		return c.denyCmd(CmdCommandRun, a.Name, empty)
	}
	if c.running.Add(1) > int32(c.maxConc) {
		c.running.Add(-1)
		return types.CommandOutput{Name: CmdCommandRun, Err: fmt.Errorf("%s: too many concurrent jobs: %w", CmdCommandRun, types.ErrInvalidArguments)}
	}
	defer c.running.Add(-1)

	timeout := a.Timeout
	if timeout <= 0 {
		timeout = c.maxRunTo
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	jobID := c.nextJobID()
	job := &CmdJob{JobID: jobID, Name: a.Name, Args: append([]string(nil), a.Args...), Started: time.Now(), cancel: cancel, done: make(chan struct{})}
	if err := c.registerJob(job); err != nil {
		return types.CommandOutput{Name: CmdCommandRun, Err: fmt.Errorf("%s: %w", CmdCommandRun, err)}
	}

	exitCode, stdout, stderr, truncated := c.execOnce(ctx, a.Name, a.Args, a.Env)
	c.mu.Lock()
	if !job.Done {
		job.ExitCode = exitCode
		job.Stdout = stdout
		job.Stderr = stderr
		job.Truncated = truncated
		job.Done = true
		close(job.done)
	}
	result := CmdResult{
		JobID:     job.JobID,
		Name:      a.Name,
		Args:      append([]string(nil), a.Args...),
		ExitCode:  job.ExitCode,
		Stdout:    job.Stdout,
		Stderr:    job.Stderr,
		Started:   job.Started,
		Duration:  time.Since(job.Started),
		Truncated: job.Truncated,
	}
	c.mu.Unlock()
	return types.CommandOutput{Name: CmdCommandRun, Value: result}
}

// execOnce 是实际执行命令的辅助函数,限制输出大小并返回 (exitCode, stdout, stderr, truncated)。
// 若 ctx 在执行中被取消,会返回 -1 表示被中断。
func (c *CmdAbility) execOnce(ctx context.Context, name string, args []string, env []string) (int, string, string, bool) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append([]string(nil), env...)
	}
	var outBuf, errBuf bytes.Buffer
	maxOut := c.maxOutput
	outLimited := &limitedWriter{w: &outBuf, remain: maxOut}
	errLimited := &limitedWriter{w: &errBuf, remain: maxOut}
	cmd.Stdout = outLimited
	cmd.Stderr = errLimited
	err := cmd.Run()
	truncated := outLimited.truncated || errLimited.truncated
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), outBuf.String(), errBuf.String(), truncated
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return -1, outBuf.String(), errBuf.String() + "\n" + ctxErr.Error(), truncated
		}
		return -1, outBuf.String(), errBuf.String() + "\n" + err.Error(), truncated
	}
	return 0, outBuf.String(), errBuf.String(), truncated
}

// limitedWriter 在写满 maxBytes 后停止写入并打上 truncated 标记。
type limitedWriter struct {
	w         io.Writer
	remain    int64
	truncated bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remain <= 0 {
		l.truncated = true
		return len(p), nil
	}
	if int64(len(p)) <= l.remain {
		n, err := l.w.Write(p)
		l.remain -= int64(n)
		return n, err
	}
	n, err := l.w.Write(p[:l.remain])
	l.remain -= int64(n)
	l.truncated = true
	return len(p), err
}

func (c *CmdAbility) startAsync(a CmdRunArgs) types.CommandOutput {
	if strings.TrimSpace(a.Name) == "" {
		return types.CommandOutput{Name: CmdCommandStart, Err: fmt.Errorf("%s: empty name: %w", CmdCommandStart, types.ErrInvalidArguments)}
	}
	if _, ok, empty := c.matchAllowlist(a.Name, a.Args); !ok {
		return c.denyCmd(CmdCommandStart, a.Name, empty)
	}
	if c.running.Add(1) > int32(c.maxConc) {
		c.running.Add(-1)
		return types.CommandOutput{Name: CmdCommandStart, Err: fmt.Errorf("%s: too many concurrent jobs: %w", CmdCommandStart, types.ErrInvalidArguments)}
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = c.maxRunTo
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	jobID := c.nextJobID()
	job := &CmdJob{JobID: jobID, Name: a.Name, Args: append([]string(nil), a.Args...), Started: time.Now(), cancel: cancel, done: make(chan struct{})}
	if err := c.registerJob(job); err != nil {
		// 登记被 closing 拒绝: goroutine 不会启动, 手动回滚并发计数
		c.running.Add(-1)
		return types.CommandOutput{Name: CmdCommandStart, Err: fmt.Errorf("%s: %w", CmdCommandStart, err)}
	}
	go func() {
		defer c.running.Add(-1)
		exitCode, stdout, stderr, truncated := c.execOnce(ctx, a.Name, a.Args, a.Env)
		c.mu.Lock()
		job.ExitCode = exitCode
		job.Stdout = stdout
		job.Stderr = stderr
		job.Truncated = truncated
		job.Done = true
		close(job.done)
		c.mu.Unlock()
	}()
	return types.CommandOutput{Name: CmdCommandStart, Value: jobID}
}

func (c *CmdAbility) waitJob(a CmdWaitArgs) types.CommandOutput {
	jobID := strings.TrimSpace(a.JobID)
	if jobID == "" {
		return types.CommandOutput{Name: CmdCommandWait, Err: fmt.Errorf("%s: empty job id: %w", CmdCommandWait, types.ErrInvalidArguments)}
	}
	c.mu.RLock()
	job, ok := c.jobs[jobID]
	wait := a.Wait
	c.mu.RUnlock()
	if !ok {
		return types.CommandOutput{Name: CmdCommandWait, Err: fmt.Errorf("%s: job %q not found: %w", CmdCommandWait, jobID, types.ErrInvalidArguments)}
	}
	if wait < 0 {
		return types.CommandOutput{Name: CmdCommandWait, Err: fmt.Errorf("%s: wait must be non-negative: %w", CmdCommandWait, types.ErrInvalidArguments)}
	}
	jobDone := job.done
	if wait == 0 {
		<-jobDone
	} else {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-jobDone:
		case <-t.C:
		}
	}
	c.mu.RLock()
	done := job.Done
	shuttingDown := c.closing
	c.mu.RUnlock()
	if !done {
		// 关闭窗口内 wait 不再视为"任务仍在运行"的普通失败: Unmount 正在
		// 取消全部运行中 job, 结果不可用——返回能力关闭哨兵, 便于调用方
		// (如远程对端/编排器)区分"等超时"与"子系统正在下线"。
		if shuttingDown {
			return types.CommandOutput{Name: CmdCommandWait, Err: fmt.Errorf("%s: job %q: %w", CmdCommandWait, jobID, cmdClosingError)}
		}
		return types.CommandOutput{Name: CmdCommandWait, Err: fmt.Errorf("%s: job %q still running: %w", CmdCommandWait, jobID, types.ErrInvalidArguments)}
	}
	result := CmdResult{
		JobID:     job.JobID,
		Name:      job.Name,
		Args:      append([]string(nil), job.Args...),
		ExitCode:  job.ExitCode,
		Stdout:    job.Stdout,
		Stderr:    job.Stderr,
		Started:   job.Started,
		Duration:  time.Since(job.Started),
		Truncated: job.Truncated,
	}
	return types.CommandOutput{Name: CmdCommandWait, Value: result}
}

func (c *CmdAbility) killJob(jobID string) (found bool, killed bool) {
	c.mu.RLock()
	job, ok := c.jobs[jobID]
	if !ok {
		c.mu.RUnlock()
		return false, false
	}
	done := job.Done
	cancel := job.cancel
	wait := job.done
	c.mu.RUnlock()
	if done {
		return true, false
	}
	if cancel != nil {
		cancel()
	}
	<-wait
	return true, true
}

func (c *CmdAbility) listJobs() []CmdJob {
	c.mu.RLock()
	out := make([]CmdJob, 0, len(c.jobs))
	for _, j := range c.jobs {
		out = append(out, j.publicView())
	}
	c.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].JobID < out[j].JobID })
	return out
}

func (c *CmdAbility) snapshotJob(jobID string) (CmdJob, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	j, ok := c.jobs[jobID]
	if !ok {
		return CmdJob{}, false
	}
	return j.publicView(), true
}

func (c *CmdAbility) clearFinished() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for id, j := range c.jobs {
		if j.Done {
			delete(c.jobs, id)
			n++
		}
	}
	return n
}

func (c *CmdAbility) nextJobID() string {
	return fmt.Sprintf("job-%d", c.jobSeq.Add(1))
}

// registerJob 在锁内复查 closing 并登记。旧实现由调用方先 rejectIfClosing
// (放锁)再登记——并发 Unmount 可夹在中间完成 beginShutdown+快照, 新 job
// 不在快照内、不被 cancel/等待, 子进程逃逸关闭。closing 判定与登记必须原子。
func (c *CmdAbility) registerJob(j *CmdJob) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return cmdClosingError
	}
	c.jobs[j.JobID] = j
	return nil
}

func (j *CmdJob) publicView() CmdJob {
	return CmdJob{
		JobID:     j.JobID,
		Name:      j.Name,
		Args:      append([]string(nil), j.Args...),
		Started:   j.Started,
		Done:      j.Done,
		ExitCode:  j.ExitCode,
		Stdout:    j.Stdout,
		Stderr:    j.Stderr,
		Truncated: j.Truncated,
	}
}

// cmdAbilityCommands 是 CmdAbility 的命令目录, 与 Command 的 case 分支一一对应。
// 二者的一致性由 TestCommandCatalogMatchesDispatch 用 AST 比对源码强制(不靠人工同步)。
var cmdAbilityCommands = []string{
	CmdCommandRun,
	CmdCommandStart,
	CmdCommandWait,
	CmdCommandKill,
	CmdCommandList,
	CmdCommandSetAllowlist,
	CmdCommandGetAllowlist,
	CmdCommandSetAllowAny,
	CmdCommandClearJobs,
	CmdCommandGetJob,
}

// ListCommands satisfies types.CommandLister. 返回副本, 调用方修改不影响目录本身。
func (c *CmdAbility) ListCommands() []string { return append([]string(nil), cmdAbilityCommands...) }

// Compile-time guarantee that CmdAbility satisfies Unmounter.
var _ types.Unmounter = (*CmdAbility)(nil)

// Compile-time guarantee that CmdAbility declares its command catalogue.
var _ types.CommandLister = (*CmdAbility)(nil)
