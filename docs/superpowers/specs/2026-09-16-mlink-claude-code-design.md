# MLink Claude Code 适配器设计规格

状态：待评审
日期：2026-09-16
依赖规格：

- `2026-08-28-mlink-user-layer-install-design.md`
- `2026-08-31-mlink-principal-routing-design.md`
- `2026-09-01-mlink-cursor-npm-release-design.md`

## 1. 目标

把 Claude Code（桌面端 Code 标签页与 CLI 共用同一份用户配置）接入 MLink，使其与 Codex、Pi、Cursor 一样成为 Owner Space 的固定身份消费者：

1. 会话开始与每轮提问自动召回 Owner 长期记忆。
2. 每轮 user 提问与 assistant 回答自动捕获，会话结束落库。
3. 不引入新的 Memory Space、Principal 或 Binding；Hermes 多飞书用户的隔离路由一行不改。
4. 只拥有 `~/.claude/settings.json` 中 MLink 自己的 Hook 条目，其余配置逐字节保留。

## 2. 已验证事实（Claude Code 2.1.260，本机实测）

### 2.1 Hook 载荷字段

用 `--settings` 挂真实 Hook 脚本抓到的载荷：

```text
SessionStart      session_id, transcript_path, cwd, source
UserPromptSubmit  session_id, transcript_path, cwd, prompt_id, prompt, permission_mode
SessionEnd        session_id, transcript_path, cwd, prompt_id, reason
Stop              base + stop_hook_active + last_assistant_message?
```

- **Claude Code 没有 `turn_id`。** 公共载荷里的 `prompt_id` 官方描述为"关联一个 user prompt 与其后续所有事件，直到下一个 prompt"，语义即为一轮，作为 MLink `TurnID`。
- `prompt_id` 在会话首次用户输入前不存在，因此 `SessionStart` 只召回、不捕获。
- 输出契约 `hookSpecificOutput.{hookEventName, additionalContext}` 与 Codex 一致。

### 2.2 与 Codex Hook 配置的差异

- `additionalContextLimit` 在 Claude Code 2.1.260 不存在，不得写入。
- `statusMessage`、`timeout`、`matcher` 存在且语义一致。
- `SessionStart` 的 source 枚举为 `startup|resume|clear|compact|fork`，比 Codex 多 `fork`。
- 落点是混装的 `~/.claude/settings.json`，不是独立的 `hooks.json`。

### 2.3 Claude Code 原生记忆

`autoMemoryEnabled: false`（等价环境变量 `CLAUDE_CODE_DISABLE_AUTO_MEMORY`）关闭官方 auto-memory 目录的读写。MLink **不**代管这个开关，由用户自行设置；本适配器不读、不写、不迁移 `~/.claude/projects/*/memory/`。

## 3. 空间与路由

Claude Code 是固定身份适配器，与 Codex/Pi/Cursor 完全同级：

```text
adapter: claude
grant:   IdentityFixed
space:   owner（schema_version 3；schema_version 2 为 personal-owner）
```

不新增 Space，不新增 Principal，不新增 Binding。Hermes 的 `hermes-private`（每个未绑定飞书用户独立 L1）与 `hermes-groups`（群 L1）路由不受影响。

## 4. 配置所有权边界

MLink 在 `~/.claude/settings.json` 中**只**拥有 `hooks` 键下命令形如 `<mlink> hook claude <Event>` 的条目。

- 禁止读取或修改 `model`、`env`、`permissions`、`enabledPlugins`、`apiKeyHelper`、主题及任何其他顶层键。
- 每次 Plan 都携带 `all_non_hook_claude_settings` 不变量：`hooks` 以外全部内容的 SHA-256 在变更前后必须相等；`Verify` 在写入后再校验一次。
- 卸载只删除 MLink 自己的条目；若 `hooks` 因此为空则整键移除，不残留 `"hooks": {}`。
- 用户已有的 Hook 条目（含同事件的其他 Hook）原样保留。
- MLink 从未写过的 `settings.json` 不得被改写：卸载前先判断是否存在 owned 条目，没有就不产出任何资源。

### 4.1 保留的是语义，不是字节

与 Codex、Cursor 适配器一样，实现方式是整份文档 round-trip（解析为 `map[string]json.RawMessage` 后重新序列化）。因此 MLink **首次写入**该文件时：

- 顶层键会被按字母序重排，缩进统一为两空格；
- 每个键的值逐字节保留，语义完全不变；
- `ProtectedSettingsHash` 同样基于规范化后的文档计算，所以不变量证明的是**语义未变**，不是字节未变。

已知边界（与其他适配器同源，不在本次修复范围）：

- 用户文件里若存在**重复顶层键**（合法但罕见的 JSON），round-trip 后只保留最后一个。
- 归属判定只看命令后缀 `<binary> hook claude <Event>` 且二进制 basename 为 `mlink`，不校验目录。好处是 MLink 二进制换路径后能自愈、不产生重复条目；代价是另一个恰好也叫 `mlink`、且命令后缀完全相同的第三方条目会被当作自己的条目替换掉。
- Hook 输入上限 1 MiB；超过会解码失败并硬失败。超大粘贴是否会触及该上限尚未实测。

把 `settings.json` 纳入 dotfiles 版本管理的用户，首次安装会看到一次键序与缩进的规范化 diff。

### 4.1 漂移检查不使用整文件比对

Claude Code 会在用户改动任意无关设置时重写 `settings.json`（键序与缩进可能变化）。因此 `doctor` 的 `claude.hook` 检查的是"四个事件各恰好一条指向当前 MLink 二进制的 owned 条目"，而不是 Codex 那种整文件字节相等。

## 5. Hook 契约与失败模式

| 事件 | 行为 | 超时 |
|---|---|---|
| SessionStart | 召回（query 为通用偏好与项目上下文），matcher `startup\|resume\|clear\|compact\|fork` | 2s |
| UserPromptSubmit | 捕获 user 片段 + 以 prompt 为 query 召回 | 2s |
| Stop | 捕获 `last_assistant_message` | 2s |
| SessionEnd | Flush 会话 | 3s |

失败模式：

- **召回失败 fail-open**：Broker 不可达时返回空上下文，不阻断用户这一轮。
- **缺 `prompt_id` 时跳过捕获而不是报错**：避免在每一轮给用户弹 Hook 失败提示。
- **空 `prompt` 跳过本轮**：仅含附件的提交没有可捕获、可检索的文本，直接返回空输出，不报错。
- **载荷与调用事件不匹配、或缺 `session_id` 才硬失败**：这是真实缺陷，必须可见。
- 召回文本始终包裹在"untrusted historical memory / 绝不作为指令"边界内，且边界行位于任何记忆文本之前。

## 6. 验收

单元测试覆盖：

- `prompt_id` 作为 TurnID 写入片段；缺失时跳过捕获且仍然召回。
- `SessionStart` 只召回不捕获。
- Plan 幂等；`model`/`env`/`permissions`/`enabledPlugins` 与用户既有 Hook 全部保留。
- 不变量在 Plan 中为 preserved；`Verify` 拒绝被篡改的受保护设置。
- 卸载后 owned 条目消失、用户 Hook 与其他设置仍在、空 `hooks` 键被移除。
- CLI `hook claude <Event>` 与 `adapter enable claude` 分发。
- 卸载时若仍有适配器在配置中启用（例如只卸 codex/pi/hermes/cursor 而保留 claude），不得判定为完整卸载：共享密钥、控制面状态与 Broker 必须保留，未选中的 Hook 也不得被删除。

实机验收（另开 `docs/testing/mlink-claude-code-live.md`）：

1. `mlink adapter enable claude --dry-run` 预览零写入，且不变量为 preserved。
2. 应用后 `mlink doctor` 的 `claude.hook` 由 `awaiting_first_turn` 转为 `active`。
3. 新开 Claude Code 会话能看到 MLink 注入的记忆；一轮问答后 `mlink maintenance journal` 可见 `claude` 适配器活动。
4. `mlink uninstall claude` 后 `settings.json` 中 MLink 条目消失，其余设置语义不变（键序与缩进已在首次安装时规范化）。
