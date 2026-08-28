# MLink Memory Connection 与 Provider Host 设计规格

- 日期：2026-08-28
- 状态：待书面审阅
- 上位规格：`docs/superpowers/specs/2026-08-28-mlink-mvp-design.md`
- 实现切片：Memory Connection 路由快照、Provider Manifest、进程外协议与 TencentDB 子进程入口

## 1. 目标

本切片把已经通过真实 MemoryCore 测试的 TencentDB Provider 放到正式的进程外边界之后，并建立后续 Mem0、Hindsight 或其他语言 Provider 可以复用的 Host 协议。

完成后应满足：

1. 每个 Provider 调用固定绑定 `connection_id + provider_id + provider_version + config_revision`。
2. Provider 由参数数组启动，不经过 Shell。
3. Broker 侧代码只依赖规范化 Provider Session，不依赖 TencentDB 类型。
4. Host 使用 UTF-8、`Content-Length` JSON-RPC 2.0，与非 Go Provider 正常通信。
5. 初始化时验证协议版本、Provider 身份、版本和运行时能力。
6. Secret 不进入命令行、环境变量、Manifest、日志或错误，只经 stdin 的 `initialize` 请求传入。
7. 请求取消、迟到响应、坏帧、进程退出和优雅关闭都有确定行为。
8. 当前 TencentDB Provider 可以由 `mlink provider run tencentdb` 作为子进程运行。

## 2. 非目标

本切片不实现：

- Broker HTTP/Unix Socket API。
- SQLite Journal、回合配对、持久化重试或幂等调度。
- Codex Hook、Pi Extension、Hermes Memory Provider。
- TUI、安装器、卸载器、Keychain UI 或 Provider 下载。
- Provider 自动重启、退避和熔断策略。
- Provider 切换事务、旧队列排空或跨 Provider 迁移。
- 任意第三方 Provider 的静默安装或强安全沙箱。
- Mem0、Hindsight、Graphiti 等第二个真实 Provider。

Host 不自动重试 `capture_turn`。重放安全由后续 Broker Journal 根据 Provider 的 `replay_safe` 描述符决定。

## 3. 方案选择

采用真实进程外方案，不先增加进程内兼容层，也不把 Broker 与 Journal 合并进本切片。

```text
ConnectionSnapshot
        │
        ▼
Provider Session API
        │
        ▼
Process Transport ── Content-Length JSON-RPC ── Provider Server
        │                                      ├─ TencentDB
        │                                      └─ non-Go fixture
        ▼
Process exit event（供后续 Supervisor 使用）
```

分层原则：

- Connection Snapshot 只固定路由和配置版本。
- Session 只管理一次已初始化的协议会话。
- Transport 只管理帧、并发请求和进程 I/O。
- Provider Server 只把规范方法转交给具体 Provider。
- 后续 Supervisor 在 Session 外实现重启和熔断，不改变 Broker 调用接口。

## 4. Memory Connection 路由模型

### 4.1 持久对象与运行快照分离

未来持久化的 `MemoryConnection` 还会包含 recall/capture policy、scope mapping 和 adapter bindings。本切片不提前冻结这些 Broker 策略，只实现 Provider 调度必须使用的不可变快照：

```go
type ProviderRef struct {
	ID      string `json:"provider_id"`
	Version string `json:"provider_version"`
}

type ConnectionSnapshot struct {
	ConnectionID   string            `json:"connection_id"`
	Provider       ProviderRef       `json:"provider"`
	ConfigRevision string            `json:"config_revision"`
	Config         json.RawMessage   `json:"config"`
	SecretRefs     map[string]string `json:"secret_refs,omitempty"`
}

type RouteKey struct {
	ConnectionID    string
	ProviderID      string
	ProviderVersion string
	ConfigRevision  string
}
```

`Config` 是非敏感配置快照。本切片由具体 Provider 的 `initialize` 做语义验证；后续 Installer 再根据 Provider Schema 做安装前结构验证。`SecretRefs` 只保存 Keychain 引用名称，不保存 Secret 值。运行时解析后的 Secret 使用独立参数传给 Session 初始化，不写回 Snapshot。

### 4.2 不可变规则

- 四个 RouteKey 字段都必须为非空、去除首尾空白后的稳定值。
- `ConnectionSnapshot.RouteKey()` 返回值副本。
- `Config` 与 `SecretRefs` 在构造时深拷贝，调用方后续修改原始 slice/map 不影响快照。
- 修改 Provider、版本、配置或 Secret 引用必须生成新的 `config_revision`。
- Session 一经初始化只绑定一个 RouteKey；不同 revision 不复用同一 Provider 进程。
- 用户身份不属于 Connection。一个 Connection 服务多个用户，请求仍必须显式携带 tenant/user/agent。

旧 Journal 事件未来使用保存的 RouteKey 查找对应 Provider 版本，不能跟随 Connection 当前活动 revision 漂移。

## 5. Provider Manifest

### 5.1 文件模型

Manifest 使用上位规格定义的 `provider.yaml`，由独立 `manifest` 包解析：

```go
type Manifest struct {
	APIVersion            string                          `yaml:"api_version"`
	ProviderID            string                          `yaml:"provider_id"`
	Name                  string                          `yaml:"name"`
	DisplayName           string                          `yaml:"display_name"`
	Version               string                          `yaml:"version"`
	Entrypoint            []string                        `yaml:"entrypoint"`
	Protocol              string                          `yaml:"protocol"`
	ProtocolVersions      []string                        `yaml:"protocol_versions"`
	BackendCompat         BackendCompatibility            `yaml:"backend_compat"`
	DeclaredCapabilities  map[string]CapabilityDescriptor `yaml:"declared_capabilities"`
	ConfigSchema          string                          `yaml:"config_schema"`
	ExecutableSHA256      string                          `yaml:"executable_sha256"`
}

type BackendCompatibility struct {
	Product     string   `yaml:"product"`
	APIVersions []string `yaml:"api_versions"`
}
```

Manifest 解析使用 `gopkg.in/yaml.v3`，启用已知字段校验。首版仅接受：

- `api_version: mlink.provider/v1`
- `protocol: stdio-jsonrpc`
- 非空、去重的 `protocol_versions`
- 非空 `provider_id`、`version` 和 `entrypoint`
- 参数数组形式的 entrypoint
- 已知结构的能力描述符

不执行环境变量、`~`、命令替换、glob 或 Shell 展开。相对可执行路径只相对 Manifest 所在 Provider 包目录解析；参数保持原样。

生产加载器本切片只接受 `executable_sha256: bundled`，并把 entrypoint 解析为当前 `mlink` 可执行文件。Python 夹具使用仅在 `_test.go` 中可构造的测试 Launcher，不形成生产侧任意代码安装入口。第三方包哈希和签名在 Installer 切片实现前，不开放生产加载。

### 5.2 能力描述符

能力不能是简单布尔值：

```go
type CapabilityDescriptor struct {
	Version         int      `json:"version" yaml:"version"`
	Scopes          []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
	Roles           []string `json:"roles,omitempty" yaml:"roles,omitempty"`
	MaxRequestBytes int64    `json:"max_request_bytes,omitempty" yaml:"max_request_bytes,omitempty"`
	MaxResultItems  int      `json:"max_result_items,omitempty" yaml:"max_result_items,omitempty"`
	MaxInFlight     int      `json:"max_in_flight,omitempty" yaml:"max_in_flight,omitempty"`
	ReplaySafe      bool     `json:"replay_safe,omitempty" yaml:"replay_safe,omitempty"`
	Ordering        string   `json:"ordering,omitempty" yaml:"ordering,omitempty"`
}
```

首版标准能力名称为 `capture_turn`、`recall`、`health`。`initialize` 与 `shutdown` 是生命周期方法，不是可选能力。

运行时能力必须满足：

- 名称存在于 Manifest。
- 版本不高于 Manifest 声明。
- 数值上限不宽于 Manifest 声明。
- scopes、roles 和 ordering 是声明值的子集。
- `replay_safe` 不能从 Manifest 的 `false` 提升为 `true`。

未知能力不会被 Host 推断或自动映射。

## 6. JSON-RPC 传输

### 6.1 帧格式

每帧格式为：

```text
Content-Length: <decimal bytes>\r\n
\r\n
<exact UTF-8 JSON bytes>
```

约束：

- Header 最大 8 KiB。
- Payload 最大 4 MiB。
- `Content-Length` 必须出现一次且为非负十进制整数。
- Payload 必须是合法 UTF-8 和单个 JSON-RPC 2.0 对象。
- 不支持批量 JSON-RPC。
- stdout 出现非帧文本、重复长度、截断 payload 或超限帧时，Session 进入 `faulted`。
- stderr 不参与协议解析。

### 6.2 请求与元数据

JSON-RPC `id` 只用于当前进程内关联响应，使用单调递增的无符号整数且不复用。业务元数据放入 params：

```go
type CallMeta struct {
	RequestID      string `json:"request_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type RequestMeta struct {
	CallMeta
	DeadlineUnixMS int64 `json:"deadline_unix_ms"`
}
```

- 每次调用必须有新的 `request_id`。
- Session 从调用 context 推导绝对 UTC deadline，不信任调用方手填毫秒值；context 没有 deadline 时使用 Host 默认值：initialize 5 秒、health 2 秒、recall 2 秒、capture_turn 5 秒、shutdown 2 秒。调用方更早的 deadline 始终优先。
- 所有发往 Provider 的调用都必须携带绝对 UTC deadline。
- `capture_turn` 必须携带 `idempotency_key`；本切片只透传，不据此重试。
- Provider 收到已经过期的 deadline 应返回稳定的 timeout 错误。

### 6.3 稳定错误

错误使用 JSON-RPC error 对象，`data.code` 必须是以下稳定值之一：

```text
configuration_error
authentication_failed
invalid_identity
unsupported_capability
deadline_exceeded
rate_limited
temporarily_unavailable
permanent_failure
ambiguous_result
protocol_error
```

`message` 是不含正文和 Secret 的简短诊断。Provider 私有错误数据默认不穿透到 Agent；Host 只保留有长度限制的安全字段。

## 7. 初始化与协议协商

Provider 进程启动后唯一允许的第一个请求是 `initialize`：

```go
type InitializeParams struct {
	Meta             RequestMeta       `json:"meta"`
	ProtocolVersions []string           `json:"protocol_versions"`
	Route            RouteKey           `json:"route"`
	Config           json.RawMessage    `json:"config"`
	Secrets          map[string]string `json:"secrets"`
}

type InitializeResult struct {
	ProviderID       string                          `json:"provider_id"`
	ProviderVersion  string                          `json:"provider_version"`
	ProtocolVersion  string                          `json:"protocol_version"`
	Capabilities     map[string]CapabilityDescriptor `json:"capabilities"`
}
```

Host 选择双方支持的最高共同 `major.minor`；当前支持 `1.0`。以下任一情况启动失败：

- 没有共同协议版本。
- 返回的 Provider ID 或版本与 Snapshot/Manifest 不一致。
- 运行时能力超出 Manifest。
- initialize 响应的任意 JSON 字符串值与某个 Secret 原值完全相同。
- initialize 超时、进程退出或输出坏帧。

初始化完成后，通用 `Secrets` map 和原始帧立即释放引用。具体 Provider 可以在其客户端中保留运行所需的 Secret；Host 为 stderr 脱敏保留只读的精确匹配副本。两者生命周期都不超过 Session，且不写入日志、错误、状态快照或后续协议请求。

## 8. Session、Transport 与进程生命周期

### 8.1 状态机

```text
new → starting → initializing → ready → stopping → stopped
                         │          │
                         └──────────┴────────→ faulted
```

- 只有 `ready` 接受 `health`、`capture_turn` 和 `recall`。
- `stopping` 后拒绝新请求。
- 读循环退出或进程异常退出时进入 `faulted`，所有 pending 调用收到同一个安全的 unavailable 错误。
- 状态转换单向进行；停止后的 Session 不复用。

### 8.2 并发与背压

- 一个 reader goroutine 读取 stdout 并按 JSON-RPC ID 分发。
- 一个独立 writer goroutine 独占 stdin，通过有界 outbound queue 接收完整帧，保证并发调用不产生交错写入。
- 普通请求入队受调用 context 控制；队列已满且 context 到期时，请求不得进入 pending 状态。
- cancel notification 使用非阻塞、尽力而为的入队；即使 Provider 不再读取 stdin，调用方取消也必须及时返回。
- pending map 有硬上限，取 Manifest/runtime `MaxInFlight` 的较小值；未声明时默认 4、最大 64。
- 达到上限时调用等待自己的 context；context 到期则不发送请求。
- 每个请求完成、取消或 Session fault 后都必须释放额度。
- writer 发生错误时 Session faulted，并终止其余 I/O；不能让 goroutine 永久阻塞在坏 Provider 的 stdin。

### 8.3 取消和迟到响应

调用方 context 结束时：

1. 从 pending map 移除该 JSON-RPC ID。
2. 释放 in-flight 额度。
3. 发送 `$/cancelRequest` notification，参数为原 ID。
4. 返回调用方 context 错误。

Provider 后续返回已移除 ID 时记录一次不含正文的迟到计数并丢弃。ID 不复用，因此不会把迟到响应交给新请求。

### 8.4 启动环境与 Secret

进程使用 `exec.Command` 参数数组，不调用 Shell。Host 显式构造最小环境：只继承 `PATH`、`LANG` 和 `LC_*`；`HOME` 设置为该 Provider revision 的 MLink 数据目录，`TMPDIR` 设置为 MLink 管理的临时目录，不继承用户真实 HOME、Agent/Broker API Key、模型地址及认证变量。工作目录固定为已验证的 Provider 包目录。

Secret 只存在于 `initialize.params.secrets`。stderr 使用 64 KiB 环形缓冲；Host 在保存诊断前按已知 Secret 原值做精确替换。响应回显检查比较解析后的 JSON 字符串值，不依赖 Secret 长度，也不扫描普通二进制子串。

Go 运行时不能承诺 Secret 内存被可靠物理清零；限制副本和释放引用只减少生命周期及意外日志暴露，不宣传为内存取证防护。

第三方 Provider 仍以当前用户权限运行，可以读取该用户有权访问的数据；stdin Secret 传递不是操作系统沙箱。

### 8.5 关闭

`Shutdown(ctx)` 在 ready 状态下发送 `shutdown`，停止接收新调用，并等待子进程在宽限期内退出。若超时：

1. 关闭 stdin。
2. 向进程发送终止信号。
3. 再次等待短宽限期。
4. 最后强制结束该子进程。

Host 为 Provider 创建独立进程组。关闭时只终止自己创建并持有句柄的精确进程组，避免遗留 Provider 子进程；不按名称、路径或模糊匹配批量结束其他进程。

### 8.6 为 Supervisor 预留的稳定边界

本切片不实现自动重启，但 Session 暴露只读 `State()`、`RouteKey()` 和只接收一次的 `Exited()` 事件。后续 Supervisor 可以创建新 Session、执行退避和熔断；Broker 继续依赖同一 Provider Session 接口。

Host 本身不保存重启次数、不 sleep 退避、不重放写请求。

## 9. Provider Session API

Broker 后续只依赖：

```go
type Session interface {
	RouteKey() connection.RouteKey
	Capabilities() map[string]manifest.CapabilityDescriptor
	Health(context.Context) (HealthResult, error)
	CaptureTurn(context.Context, CallMeta, model.Turn) (model.WriteReceipt, error)
	Recall(context.Context, CallMeta, model.RecallRequest) (model.ContextBundle, error)
	Shutdown(context.Context) error
	State() State
	Exited() <-chan ExitEvent
}
```

Host 在发送前验证：

- 调用所需能力存在。
- 请求 identity 的 `ConnectionID` 与 Session RouteKey 一致。
- 请求大小不超过协商限制。
- capture 有非空 idempotency key。
- deadline 尚未过期。

Host 不做语义去重、时间排序、token 预算、用户 ID 派生或 Provider 专有结果修复。

## 10. Provider Server 与 TencentDB 子命令

增加语言无关的 Server 调度层。Handler 接口实现 `Initialize`、`Health`、`CaptureTurn`、`Recall` 和 `Shutdown`。Server 负责帧和 JSON-RPC，Handler 负责业务。

`mlink provider run tencentdb` 的行为：

1. 启动时不读取 Agent 配置，也不要求模型相关环境变量。
2. 等待 `initialize`。
3. 从非敏感 config 读取 MemoryCore `base_url`、`service_id` 和超时。
4. 从 `secrets` 读取 `token`，构造现有 `tencentdb.Client` 与 Provider。
5. 返回真实运行时能力：`health`、`capture_turn`、`recall`；`capture_turn.replay_safe=false`。
6. 后续请求复用已经初始化的 Provider。
7. shutdown 后清除引用并退出。

MemoryCore 内部 LLM/Embedding 设置不属于该子命令配置；MLink 不读取或修改 Codex、Pi、Hermes 的模型地址、Key 或登录状态。

## 11. 外部 Provider 与可扩展性

协议包不能导入 TencentDB 包。外部 Provider 只需实现同一 framing 和方法，不需要链接 Go 代码。

本切片增加一个 Python 最小 Provider 夹具，覆盖 initialize、health、capture、recall、cancel 和 shutdown。测试环境必须提供 `python3`；缺失时给出确定性测试失败和安装提示，不把核心跨语言契约静默 skip。

未来远程 Provider 通过新增 Transport 实现接入。Broker 和 Agent Adapter 仍依赖 Session API，不直接依赖 `exec.Cmd` 或 stdio。

## 12. 故障语义

| 故障 | 本切片行为 |
|---|---|
| Manifest 非法 | 启动前拒绝，不执行 entrypoint |
| entrypoint 不存在 | 返回稳定配置错误 |
| 无共同协议版本 | 终止子进程，Session faulted |
| Provider 身份/版本不一致 | 终止子进程，Session faulted |
| 能力越权 | 终止子进程，Session faulted |
| Secret 回显 | 终止子进程，只返回安全错误 |
| stdout 垃圾或坏帧 | Session faulted，pending 全部失败 |
| stderr 过量 | 只保留末尾 64 KiB，不阻塞子进程 |
| 请求 context 到期 | cancel、删除 pending、丢弃迟到响应 |
| Provider 异常退出 | Session faulted，发出 ExitEvent |
| shutdown 超时 | 精确终止当前子进程 |
| capture 结果未知 | 返回 ambiguous 类错误；Host 不重试 |

Adapter 的最终 fail-open 行为属于后续 Broker/Adapter 切片；本切片必须保留足够错误类型让调用方正确降级。

## 13. 测试策略

### 13.1 Connection

- 缺失任一 RouteKey 字段时拒绝。
- Config 与 SecretRefs 构造后深拷贝。
- 不同 config revision 产生不同 RouteKey。
- user/session/turn 不进入 RouteKey。

### 13.2 Manifest

- 正常 provider.yaml 可加载。
- 未知字段、重复协议版本、空 entrypoint、Shell 字符串形式、非法能力约束均拒绝。
- entrypoint 中的 `$()`、glob 和分号作为普通参数，绝不执行。
- 运行时能力越权拒绝。

### 13.3 Codec

- 多行中文 JSON、分片读取和连续多帧正常。
- header 超限、payload 超限、重复/缺失 Content-Length、非法 UTF-8、截断、stdout 垃圾全部失败。
- writer 在并发调用下不产生交错帧。

### 13.4 Session

- initialize 必须是第一个请求。
- ID/版本/协议/能力协商成功和失败路径。
- pending 上限和 context 等待。
- 进行中取消会发送 notification，迟到响应被丢弃。
- 子进程退出会让所有 pending 调用及时失败。
- shutdown 正常退出和超时强制结束。
- Secret 不出现在 argv、env、响应错误、stderr 诊断和测试日志。

### 13.5 跨语言与 TencentDB

- Python Provider 通过完整生命周期契约。
- 恶意 Python 夹具分别输出垃圾、超大帧、错误身份、能力越权和 Secret 回显，Host 均拒绝。
- 构建 `mlink` 后以子进程启动 TencentDB Provider，使用 `httptest.Server` 作为 MemoryCore v3 Gateway，验证 initialize → health → capture_turn → recall → shutdown。
- 在本地隔离 MemoryCore 实例上做一次真实子进程烟雾测试，凭据只通过 stdin。

## 14. 验收标准

本切片完成必须同时满足：

1. Connection Snapshot 与 RouteKey 测试通过。
2. Manifest 和能力子集校验通过。
3. Codec 对正常、分片、并发、恶意和超限输入测试通过。
4. Python 非 Go Provider 完整生命周期通过。
5. TencentDB Provider 通过真实 `mlink provider run tencentdb` 子进程调用。
6. context 取消、迟到响应、异常退出和 shutdown 超时测试通过。
7. Secret canary 扫描证明 argv、env、错误、诊断和仓库均无泄漏。
8. `go test ./...`、`go test -race ./...`、`go vet ./...` 和 `gofmt -d .` 通过。
9. 用户已有 README 修改不被覆盖或提交。
10. Codex、Pi、Hermes 模型和认证配置没有任何变化。

通过这些条件只表示 Provider Host 切片可进入 Broker/Journal 阶段，不表示 MLink MVP 已完成。

## 15. 后续演进边界

- **Supervisor**：监听 `Exited()`，实现退避、熔断和健康恢复，不改变 Session 协议。
- **Broker Journal**：持久化 RouteKey、idempotency key 和回执状态；决定是否重试。
- **Provider 切换**：创建并验证新 revision，再原子激活；旧事件继续使用旧 RouteKey。
- **远程 Transport**：新增实现，不改变 Broker、Connection 或 Agent Adapter。
- **第三方安装**：在 Manifest 外增加签名、哈希、发布者和用户确认，不把声明当作沙箱。
- **更多 Provider**：只新增 Provider Server Handler 和 Manifest，不修改 Agent Adapter。

以上边界确保当前实现不会把 TencentDB 层级、进程策略、重试语义或 Agent 模型配置固化进通用接口。
