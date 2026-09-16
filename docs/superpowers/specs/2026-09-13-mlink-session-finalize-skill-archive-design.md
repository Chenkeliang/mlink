# MLink Session Finalize → TencentDB Skill Archive 设计

## 目标

Codex、Cursor、Pi 发出正式 SessionEnd 后，MLink 持久化一个 finalize 请求。只有该 session 之前的完整 Turn 均已完成 Provider 投递，Broker 才调用 TencentDB MemoryCore `skill/conversation/force-archive`。进程重启、临时错误和重复 SessionEnd 不得丢失或重复破坏状态。

Hermes 当前没有可靠的单会话结束事件，继续使用已上线的十分钟空闲归档与 Provider 退出补归档。

## 非目标

- 不识别自然语言“记住”意图。
- 不采集 `tool_call/tool_result`。
- 不读取 Codex、Cursor、Pi 的非稳定 transcript 格式。
- 不改变普通记忆、Skill 提炼模型或 MemoryCore 存储格式。

## 生命周期

```text
Agent SessionEnd
  → Broker Flush（清理未配对 fragment，读取未完成 Turn 数）
  → Journal RequestFinalize（持久化 scope + Provider route）
  → Worker 继续投递此前 Turn
  → 无 pending Turn、无未解决 permanent/ambiguous Turn
  → ClaimFinalize
  → Provider archive_session（幂等）
  → MemoryCore skill/conversation/force-archive
  → MarkFinalizeCompleted
```

SessionEnd Hook 只需提交 finalize 请求，不在 Hook 的有限超时时间内等待 LLM 提炼。HTTP 成功表示 finalize 已持久化，不表示 Skill 已生成。

## Journal

新增 `session_finalizations` 表，主键为 `(adapter_id, connection_id, tenant_id, agent_id, user_id, session_id)`，保存：

- Provider route：`provider_id/provider_version/config_revision`；
- 身份：`tenant_id/user_id/agent_id/session_id` 与 `actor_digest`；
- 状态：`queued/dispatching/retryable_failed/completed/permanent_failed/ambiguous`；
- `attempt_count/next_attempt_at/error_code/created_at/updated_at`。

重复 SessionEnd 在尚未完成时保持同一请求；已完成后再次请求允许重新排队，因为同一个 session 可能被 resume 后新增 Turn。MemoryCore force-archive 对空 buffer 返回成功，因此重复归档安全。

Claim 查询必须同时满足：

1. finalize 处于 `queued` 或到期的 `retryable_failed`；
2. 同 adapter/session 不存在 `queued/dispatching/retryable_failed` Turn；
3. 不存在尚未 resolution 的 `permanent_failed/ambiguous` Turn；
4. 不存在完整但尚未转成 Turn 的 fragment 对。

## Provider 协议

在现有 1.0 协议上增加可选 capability `archive_session`，而不是提升所有 Provider 的必选接口：

- 请求：完整 `IdentityScope`；调用元数据包含稳定 idempotency key；
- 返回：`archived=true`；
- capability 声明 `replay_safe=true`、`ordering=session`；
- TencentDB Provider 将其映射到 `skill/conversation/force-archive`，成功后取消本地空闲计时器并忘记该 session；
- 不支持该 capability 的 Provider 返回 unsupported，Broker 将 finalize 标记 completed（该 Provider 没有 Skill buffer 可归档）。

## Worker 失败处理

- 未发送或 replay-safe 的超时、限流、临时不可用：`retryable_failed`，沿用 Turn 指数退避。
- 已发送但无法确认且 capability 未声明 replay-safe：`ambiguous`。
- 永久错误：`permanent_failed`。
- 成功或 Provider 不支持：`completed`。

Worker 每次处理 Turn 后再尝试处理一个 finalize；没有 Turn 时也会处理 finalize。这样最后一个 Turn 被标记 accepted 后，同一 Drain 周期即可触发归档。

## 安全与隔离

- finalize 请求使用 Broker 已解析的规范化身份，不接受 Adapter 直接传入 TencentDB ID。
- Claim 与 Provider 调用均携带完整 route 和身份，不能仅按裸 `session_id` 归档。
- 日志、错误和 API 响应不暴露 Provider token、后端正文或原始平台身份。
- 现有十分钟空闲归档保留，作为 Hermes 和异常缺失 SessionEnd 的兜底；generation 检查继续防止旧计时器归档新 Turn。

## 验收

1. 无 pending Turn 时，Codex/Cursor/Pi SessionEnd 请求持久化并归档一次。
2. Turn 尚在 queued/dispatching/retryable 时不得提前归档；Turn accepted 后自动归档。
3. unresolved permanent/ambiguous Turn 阻止归档；显式解决后允许继续。
4. Broker 在 finalize 请求后重启，仍能完成归档。
5. 重复 SessionEnd 幂等；session resume 后再次 SessionEnd 可产生新一轮空归档。
6. archive 暂时失败按 replay-safe 规则重试；新 Turn 到达使旧空闲 timer 失效。
7. Hermes 行为不变；无 `archive_session` capability 的 Provider 继续正常工作。
8. 全量、随机顺序、race、vet、Provider 子进程和真实候选 Doctor 通过。
