<div align="center">
  <img src="https://avatars.githubusercontent.com/u/245985800?s=200&v=4" style="width:100px;" width="100"/>
  <h3>FasterEdge - 对称、可靠、安全的多场景边缘计算框架</h3>
</div>

### 项目简介

- “我们把每棵树想象成是一个设备上的服务（Atom），这棵树有各式各样的根（Data)，这棵树可以从根中吸取养分，并将养分输送至枝干（Ability），枝干上或许有几个鸟巢或蜂窝，这些小家伙会将那‘那些东西’从一棵树带到另一棵树，而‘那些东西’在自己这棵树上是完全可以被任何部分获取的。”
    
- 当然这是一颗可大可小的树，甚至支持“水培”，以适配各种各样的“生存环境”。您可以开发任何自定义的树枝（Ability）或根系（Data），从而实现一定程度的“极致优化”，从而打到不一样的结果。

- 您可以直接使用Go语言版实现大多数平台的打包和部署，或者使用Rust语言版（未推出）、C++语言版（未推出）实现更高性能的边缘计算服务，当然他们之间也是可以相互交互的。

- 这意味着您可以通过这个框架（或者说这种符合“依赖抽象而不依赖具体”的强规范）下的“树”来构建一个相当灵活的分布式的边缘计算系统，甚至是一个分布式的边缘计算生态系统。

<div align="center">
  <img src="./concept.png" style="width:96%;" width="100"/>
</div>

- 图中的小女孩代表着业务或用户（Business/User），这些树枝（Ability）是由树根（Data）提供养分生长出来的。 

### 术语

### 开发模式

### 生态系统

### 设计哲学
- 依赖抽象而不依赖具体（Depend on Abstractions, Not on Concrete Implementations.）
- 遵循策略模式（Strategy Pattern）、 命令模式（Command Pattern）、 组合模式（Composite Pattern）

### 生命周期与优雅退出

FasterEdge 是库，不是独立进程。先注册组件，再挂载，最后在可取消的上下文中运行：

```go
atom, err := fasteredge.InitAtom()
if err != nil { return err }
if err := fasteredge.PreRunAtom(atom); err != nil { return err }
return fasteredge.RunAtom(ctx, atom, fasteredge.WithShutdownTimeout(5*time.Second))
```

`RunAtom` 会监督实现 `types.Runner` 的 Ability；上下文取消或 Runner 返回错误后，框架使用新的清理上下文按逆序卸载组件。关闭超时、panic 和卸载错误均通过结构化 `error` 返回，组件注册表不会暴露内部 map。

TimeAbility 的命令参数使用严格类型（例如 `TimeSyncManualArgs`），时间缓存基于单调时钟推进。网络时间源默认拒绝本机、私网、链路本地和组播地址，并禁用环境代理；只有显式启用 LAN 源选项时才放行私网地址。HTTP 响应限制为 64 KiB，NTP 使用校验后的响应偏移量。


    
