# MLink 稳定主体、多别名绑定与 Hermes 空间路由设计规格

状态：待用户审阅  
日期：2026-08-31  
依赖规格：

- `2026-08-28-mlink-mvp-design.md`
- `2026-08-28-mlink-provider-host-design.md`
- `2026-08-28-mlink-user-layer-install-design.md`

## 1. 目标

本规格替代原用户层规格中“一个 Connection 同时承载 Provider、身份和共享范围”的简化设计，补齐安装前发现的身份连续性和 Hermes 群聊路由边界：

1. Codex、Pi 与用户本人的 Hermes 私聊共享同一个稳定个人主体及其 L1/L2/L3。
2. 其他 Hermes 私聊用户各自拥有隔离的个人 L1，不召回 L2/L3。
3. Hermes 群聊不写入任何成员的个人记忆，而以群为长期记忆主体形成群 L1。
4. 同一群的主会话和不同话题使用不同 Session，但共享同一份群 L1。
5. 群空间不召回 L2/L3，避免 TencentDB 当前 `team_id + agent_id` 共享语义生成造成画像污染。
6. 飞书 ID 变化时，通过给稳定主体增加新别名继续访问原记忆，不迁移、不复制 MemoryCore 数据。
7. 支持安全列出、绑定、换绑、撤销、导出和恢复身份配置。
8. 只有完成本规格的离线、隔离后端和真实 Hermes 验收，才重新生成本机安装 Plan。

## 2. 已验证事实

### 2.1 Hermes v0.20.5 运行字段

本机 OrbStack `hermes-agent-env` 的真实 Session 与 Gateway Routing 已验证会提供：

```text
platform
chat_type
chat_id
thread_id
user_id
user_id_alt
gateway_session_key
```

Feishu Adapter 的来源映射为：

```text
platform   = feishu
chat_id    = message.chat_id
thread_id  = message.thread_id 或 message.root_id
user_id    = tenant user_id，缺失时退回 open_id
user_id_alt = union_id
```

真实数据验证结果：

- 32 个 Feishu Session 的 `chat_id/user_id` 均非空且与 `origin_json` 一致。
- `user_id_alt` 在 31/32 个历史 Session 中存在；缺失时仍有主 `user_id`。
- 8 个私聊话题使用同一个私聊 `chat_id` 和各不相同的 `thread_id`。
- 1 个真实群话题同时具有 `chat_type=group`、稳定 `chat_id` 与稳定 `thread_id`。
- 当前 `thread_sessions_per_user=false` 的群话题 Session Key 不含用户 ID。

### 2.2 当前 Hermes Session 策略

用户确认群聊是共同对话空间，采用：

```yaml
group_sessions_per_user: false
thread_sessions_per_user: false
```

MLink Installer 将 `group_sessions_per_user` 从当前 `true` 语义合并为 `false`；`thread_sessions_per_user` 显式设为 `false`。两项都是 MLink 拥有的 Hermes 配置片段，卸载时恢复安装前值。模型、Provider、Base URL、认证和其他 Hermes 配置仍是受保护字段。

### 2.3 TencentDB MemoryCore v2.0.1 边界

- L1 可以按 `team_id + agent_id + user_id` 提供用户隔离。
- L2/L3 的实测有效范围接近 `team_id + agent_id`，不是用户私有。
- 单独更换 `x-tdai-service-id` 不能作为 L3 隔离保证。
- MLink 只把 L2/L3 暴露为 Agent 共享层，并由显式策略决定是否召回。
- MemoryCore 请求只收到 MLink canonical `team_id/agent_id/user_id/session_id`；飞书原始身份不发送给 Provider。

## 3. 核心模型

### 3.1 Connection 与 Memory Space 解耦

`Connection` 只描述 Provider 传输和版本：

```text
Connection
  id
  provider_id
  provider_version
  config_revision
  provider_config
  secret_refs
```

新增 `MemorySpace` 描述长期记忆身份和共享策略：

```text
MemorySpace
  id
  connection_id
  tenant_id
  agent_id
  include_agent_shared
  principal_policy
```

一个 TencentDB Connection 可以承载多个逻辑 Space，无需复制 Provider 进程或 Token。

### 3.2 Principal

`Principal` 是 MLink 内部稳定记忆主体，不等于外部账号：

```text
Principal
  id             # MLink 管理 ID，例如 principal_owner
  canonical_user # 发给 MemoryCore，例如 usr_owner_<random>
  kind           # person | external_user | group
```

`canonical_user` 在首次 Apply 时生成一次，之后重装、换绑、Broker 重启和配置恢复均不得改变。

### 3.3 Binding

`Binding` 把外部身份别名映射到 Principal：

```text
Binding
  source       # local | feishu
  kind         # codex | pi | union_id | user_id | open_id
  value        # 原始稳定外部 ID，仅存 Keychain
  principal_id
  status       # active | revoked
```

同一个 Principal 可持有多个 active Binding：

```text
local:codex            ─┐
local:pi               ─┼─ principal_owner ─ usr_owner_<stable>
feishu:union_id:old    ─┤
feishu:union_id:new    ─┘
```

换飞书 ID 只增加或撤销 Binding，不改变 `canonical_user`，也不复制 MemoryCore 数据。

## 4. 三类空间与路由

### 4.1 个人 Owner Space

```text
space_id             = personal-owner
tenant_id            = personal
agent_id             = keliang-personal
include_agent_shared = true
principal            = principal_owner
```

消费者：

- Codex
- Pi
- 与 `principal_owner` Binding 匹配的 Hermes 私聊

该 Space 可以召回 L1/L2/L3；L2/L3 只服务这个个人 Space。

### 4.2 其他 Hermes 私聊 Space

```text
space_id             = hermes-private
tenant_id            = personal
agent_id             = hermes-private
include_agent_shared = false
principal            = usr_<HMAC(platform + stable external subject)>
```

每个未绑定到 owner 的私聊用户得到独立 L1。缺少 `user_id_alt` 时可使用 `user_id`；两者均缺失则 fail-open：不召回、不捕获，不回退昵称、chat ID、静态 `default` 或上一个用户。

### 4.3 Hermes Group Space

```text
space_id             = hermes-groups
tenant_id            = personal
agent_id             = hermes-groups
include_agent_shared = false
principal            = grp_<HMAC(platform + chat_id)>
```

所有成员在同一群中共同写入并读取群 L1。群消息绝不写入发言人的个人 Principal。不同群由 `chat_id` 派生不同 Principal。

同一群的 Session：

```text
群主会话：ses_<HMAC(platform + chat_id)>
群话题：  ses_<HMAC(platform + chat_id + thread_id)>
```

话题只隔离短期对话与 L0 Session；同一群的所有话题共享群 Principal 和群 L1。

## 5. Hermes 请求与 Broker 决策

Hermes Provider 不再只发送一个 `source_subject`，而发送完整、受 Broker 授权的外部上下文：

```text
adapter_id
source
chat_type
chat_id
thread_id
primary_subject
alternate_subject
session_id
```

Broker 决策顺序不可配置为模糊回退：

```text
if source != feishu:
    reject

if chat_type == group:
    require chat_id
    route hermes-groups
    principal = HMAC(platform, chat_id)
    session = HMAC(platform, chat_id, optional thread_id)

else if chat_type == dm and (alternate_subject or primary_subject) matches owner Binding:
    route personal-owner
    principal = principal_owner
    session = HMAC(platform, chat_id, optional thread_id)

else if chat_type == dm and (alternate_subject or primary_subject) exists:
    route hermes-private
    principal = HMAC(platform, stable subject)
    session = HMAC(platform, chat_id, optional thread_id)

else:
    unsupported_or_missing_identity; fail-open without memory
```

`chat_name`、`user_name` 和消息展示昵称永不参与身份决策。

## 6. 群消息和说话人

MemoryCore 的群请求使用群 canonical `user_id`，不使用发言人的个人 `user_id`。发言人的原始飞书 ID不进入 Provider 请求和记忆正文。

MLink Journal 可记录当前发言人的短 HMAC `actor_digest` 以便事件冲突检测和本地审计；它不是 MemoryCore Identity，也不进入召回文本。群事件幂等键同时包含 group Principal、Session、Turn 和内容哈希。

卡片按钮等合成事件若没有 `thread_id`：

- 长期记忆仍路由到同一群 Principal，因此不会进入个人空间或错误群。
- Session 降级为群主会话并记录 `thread_context_missing` 诊断。
- 不解析不稳定的 Hermes Session Key 来猜测话题 ID。

## 7. 存储与隐私

非秘密配置保存：

- Principal ID 与 canonical user ID
- Memory Space 与 Connection 路由
- Binding 的 Keychain 引用、类型、状态和非敏感指纹

macOS Keychain 保存：

- MemoryCore Token
- 32 字节 MLink Identity HMAC Key
- Hermes Broker Grant
- owner 的原始 Feishu `union_id/user_id/open_id` 别名集合

日志、ChangeSet、SQLite Journal 和备份索引不得包含原始飞书 ID、群 ID、话题 ID或 Keychain Secret。TUI 只显示名称、来源和 ID 后四位；名称仅用于人工选择，不参与匹配。

## 8. 身份管理 CLI/TUI

公共命令：

```text
mlink identity list [--json]
mlink identity bind --principal owner --source feishu --stdin
mlink identity rebind --principal owner --source feishu --stdin
mlink identity revoke <binding-id>
mlink identity export --output <path>
mlink identity import <path> --dry-run
mlink identity import <path>
```

- Secret 或原始外部 ID 只通过 masked TTY/stdin，不接受 argv 明文。
- `list` 只显示 Binding 指纹和末四位。
- `bind/rebind/revoke/import` 使用 ChangeSet、预览、确认、备份、Apply 和回滚。
- 安装 TUI 从 Hermes `state.db` 与 `gateway_routing` 只读提取去重候选，用户明确选择“陈科良”；不得按昵称自动绑定。

## 9. 加密身份导出与恢复

为支持换机且保持 canonical Identity，提供版本化加密 Bundle：

```text
MLinkIdentityBundleV1
  schema_version
  principals
  spaces
  identity_hmac_key
  bindings
  created_at
```

- Bundle 使用随机 Salt、`scrypt` 派生 256 位密钥和 AES-256-GCM 加密。
- 导出密码只从 masked TTY/stdin 获取，不接受 argv。
- 输出文件原子创建并强制 `0600`。
- Bundle 不包含 MemoryCore Token、Hermes Broker Grant或 Agent 模型凭据。
- Import 先展示 Principal、Space 和 Binding 指纹 diff；冲突或 canonical user 不一致时拒绝。
- Apply 在单一事务中恢复非秘密配置与 Keychain 身份材料，失败时回滚。

## 10. 安装与升级

现有 `plan_5b496cbe0ed3a1c09525934f9c` 永久作废，不得 Apply。

新 Installer：

1. 只读检测 Hermes Session 候选。
2. 用户选择 owner 飞书身份。
3. 创建一个 Connection、三个 Memory Space 和 owner Principal。
4. 将 Codex/Pi 固定绑定 owner。
5. 将选中的飞书别名保存在 Keychain。
6. 语义合并 Hermes 两个 Session 策略为 `false`。
7. 展示新的完整 ChangeSet。
8. 用户确认后才写入、启动 Broker并执行验收。

由于当前 MLink 尚未安装，配置 Schema 可以直接升级为 v2；仍需测试 v1 文件能被只读识别并给出明确迁移预览，不能静默重解释旧身份。

## 11. 错误处理

- 未绑定 owner ID：该 Feishu 私聊作为普通隔离用户，不得自动合并 owner。
- owner ID 变化：显示未绑定候选；用户显式 rebind 后恢复访问原 Principal。
- Keychain Identity Key 缺失或长度错误：Broker 拒绝启动，不生成新 Key 代替。
- Binding 冲突到两个 Principal：拒绝加载配置和 Apply。
- 群缺失 `chat_id`：本轮不召回、不捕获，记录 `group_identity_missing`。
- 话题缺失 `thread_id`：允许群 L1，Session 降级为群主会话并给 Doctor 警告。
- 原始外部 ID 出现在日志、Journal 或 ChangeSet：测试失败并拒绝提交。

## 12. 强制验收门槛

以下 10 项全部通过后才能生成新的真实安装 Plan：

1. 旧飞书 ID 绑定 owner，在隔离 MemoryCore Space 写入随机 Canary。
2. Codex、Pi 和旧飞书 ID 均能召回该 Canary。
3. 新飞书 ID 绑定到相同 owner，不改变 canonical user。
4. 新飞书 ID 无数据迁移即可召回原 Canary。
5. Broker 重启后所有绑定与 canonical user 保持不变。
6. 撤销旧 ID 后旧 ID 被拒绝，新 ID 仍能召回。
7. 未绑定用户不能读取 owner Canary。
8. 群 Principal 不能读取 owner Canary，owner 不能读取群 Canary。
9. 加密导出并恢复身份 Bundle 后 canonical user 与所有 Binding 保持不变。
10. MemoryCore 中未因换绑产生第二个个人 Space 或复制 Canary。

额外群聊验收：

- 同一群不同用户进入相同群主 Session。
- 同一群话题不同用户进入相同话题 Session。
- 同一群不同话题的 Session 不同，但群 Principal 相同。
- 不同群的 Principal 不同。
- 群消息只形成群 L0/L1，不形成任何成员个人 L1。
- 群和普通 Hermes 私聊均不召回 L2/L3。

## 13. 非目标

- 不修改 TencentDB MemoryCore 源码或官方 Hermes 源码。
- 不根据姓名、昵称、邮箱或消息文本自动合并身份。
- 不把群 L1 同步到个人 L1，也不把个人 L1 注入群聊。
- 不为群启用 L2/L3。
- 不迁移或删除现有 HyMemory、`MEMORY.md`、`USER.md` 数据。
- 不在本轮实现 Web 管理后台、Mem0、Homebrew/npm 分发或 HyMemory 数据迁移。

## 14. 成功标准

完成后，MLink 的外部 Agent 与记忆空间关系固定为：

```text
Codex ───────────────┐
Pi ──────────────────┼─ personal-owner ─ L1/L2/L3
Hermes owner 私聊 ───┘

Hermes 其他私聊用户 ─── hermes-private/<user> ─ L1 only

Hermes 群主会话 ───────┐
Hermes 群话题 ─────────┼─ hermes-groups/<chat> ─ group L1 only
同群其他成员 ──────────┘
```

任何外部 ID 变化只影响 Binding，不改变 MemoryCore canonical Identity；任何群消息只属于群 Principal，不属于发言人的个人长期记忆。
