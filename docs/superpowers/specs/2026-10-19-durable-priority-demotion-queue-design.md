# CPAM 持久化服务器错误优先级降级队列设计

- 日期：2026-10-19
- 项目：CPA Manager Plus（CPAM）
- 状态：待 LO 审阅
- 约束：只修改 CPAM；不修改、重启、构建或部署 CPA Core / `cli-proxy-api`
- 基线：`e3a3d8a0 fix: reconcile stale proxy pool states`

## 1. 背景与问题边界

CPAM 当前在内存 channel 中接收符合条件的失败事件，channel 容量为 256，且只有一个执行 worker。高并发时会出现：

```text
[server-error-priority] job queue full, dropped auth file ...
```

服务器实测在 2026-10-19 00:00 之后出现约 8,199 次降级和 4,519 次队列丢弃。丢弃意味着已经落入 `usage_events` 的符合条件事件没有执行对应的优先级 `-1`，也没有可恢复的任务记录。

本设计只解决 CPAM 的“服务器错误优先级降级”后台执行可靠性与吞吐，不改变：

- CPA Core 的调度、凭证读取、计费、额度、重试和接口语义；
- CPAM 的用量事件原始记录、价格计算、历史统计、quota snapshot 或 dashboard rollup；
- 已批准的错误匹配规则：HTTP 502、HTTP 503、HTTP 429 且 JSON `detail` 精确为 `Rate limit exceeded`，以及现有的两个精确 HTTP 500 Codex transport failure 文本；
- 已批准的优先级策略：排除目标后取最高 peer priority；若 peer 最大值低于当前值则写 `peer_max - 1`，否则写当前值 `- 1`，结果始终不低于 0。

## 2. 目标与验收条件

### 目标

1. 每个新符合条件的事件都有一个持久化任务，不能因为内存 channel 满而静默丢弃。
2. 同一账号（以 CPAM auth 文件和 provider/auth index 组成的 credential key）严格按事件顺序串行；不同账号最多 8 路并发。
3. Manager Server 重启、worker 崩溃、CPA 请求超时后，任务可恢复；不会因为“CPA 已经写入但 CPAM 尚未标记完成”而重复降级同一事件。
4. 10 个独立事件必须保留为 10 个任务，不能合并为一次 `-10` 或按账号去重。
5. 现有队列满丢弃日志消失，改为可观察的 admitted/claimed/completed/retry/failed 统计。
6. 现有开关继续生效：关闭后的新事件不入队；关闭前已经持久化的任务暂停且保留；重新打开后继续处理这批 backlog，并只接受重新开启后的新事件。
7. 首次部署不回放当前已经存在的 4,519 条历史丢弃事件，也不对历史 `usage_events` 做批量降级。

### 非目标

- 不补偿部署前已经丢弃的事件；用户已明确“不需要”历史回放。
- 不引入 CPA Core 的改动或新的公网调用路径。
- 不把管理密钥、完整认证 JSON、access token 写入新任务表或日志。
- 不新增前端页面；本次先保证后端执行正确、可审计，必要的 UI/指标另行设计。

## 3. 方案比较与选择

### A：扩大内存 channel

把容量从 256 提高到数千，只保留一个 worker。实现最小，但进程重启仍会丢任务，且突发量超过容量时继续丢弃；不能满足严格 10 次语义。

### B：扩大 channel + 多 worker

可以提高吞吐，但仍然是易失队列；多个 worker 还会引入同一凭证乱序、重复写入和 peer inventory 请求风暴。容量只能延缓而不能消除丢失。

### C：SQLite 持久队列 + 8 路受控 worker（采用）

任务先落 CPAM 已有 SQLite，再由 8 个 worker 按数据库规则领取。SQLite 是权威队列，内存 wake channel 只用于唤醒，满了可以合并唤醒信号而不能丢任务。通过 lease、事件哈希唯一约束、同 credential FIFO 和写入前后的 priority 记录处理崩溃恢复。

## 4. 总体架构

```text
CPA request event
      │
      ▼
usage_events（现有，原子持久化；不改计费语义）
      │
      ├─ 当前 collector fanout ──► EnqueueCandidates（INSERT OR IGNORE）
      │                                 │
      │                                 ▼
      │                 server_error_priority_demotion_jobs
      │                                 │
      │                 8 workers ◄─────┴─────► wake signal（可合并）
      │                    │
      │                    ├─ credential FIFO claim
      │                    ├─ MutationCoordinator（保留）
      │                    ├─ CPA management API patch
      │                    └─ complete/retry/failed
      │
      └─ reconciler：按已启用区间与 cursor 扫描部署后新 usage_events，修复进程崩溃造成的
         “usage_events 已提交但 Enqueue 尚未提交”的极小窗口；开关关闭期间的事件不会补入
```

### 4.1 入队与崩溃窗口

当前 collector 先提交 `usage_events`，再把新事件交给 automation fanout。新队列在 fanout 中做短事务 `INSERT OR IGNORE`，`event_hash` 是唯一键，因此重复通知不会生成第二个任务。

为覆盖 CPAM 在两个事务之间崩溃的窗口，队列记录一个只前进 cursor，并记录开关实际生效的事件 ID 区间：

- 首次初始化发生在 HTTP listener/collector 启动之前。它读取当时 `usage_events` 的最大 `id` 作为 `source_floor_event_id`；任何 `id <= floor` 的历史事件永远不回放；
- 当开关开启时，在一个短 SQLite 事务中读取当前最大 `id`，新建一个从 `max(id)+1` 开始的 enabled interval；只有落在该 interval 的事件才有资格被 live fanout 或 reconciler 入队；
- 当开关关闭时，在一个短 SQLite 事务中以当前最大 `id` 封闭 active interval，并把 cursor 前移到该边界；关闭后新产生的事件不在 enabled interval，既不会 live 入队，也不会在后续重新开启时被回扫；
- 后台 reconciler 只按 `id > cursor` 固定小批量读取必要字段，并只对 enabled interval 内的事件调用同一资格函数；cursor 和本批次的 `INSERT OR IGNORE` 在同一事务提交；
- 如果 live fanout 已入队，唯一哈希使 reconciler 无副作用；如果进程在 `usage_events` 提交后、live fanout 入队前崩溃，reconciler 会补上任务；如果开关在这期间关闭，区间边界决定该事件是否应保留，不能由陈旧内存状态决定。

关闭时 worker 不再领取新任务，已入队 backlog 保留；重新打开后先恢复这批 backlog，再接收落在新 enabled interval 的实时事件。这既保留了原开关“关闭后不再触发降级”的语义，也不以静默丢弃覆盖已确认入队的工作。

### 4.2 资格判定

live fanout 和 reconciler 必须调用同一个纯函数，不允许复制两套规则：

- `failed = true` 且 status `502` 或 `503`；
- status `429` 且 `FailBody` 可解析为 JSON，并且 `detail == "Rate limit exceeded"`；
- status `500` 且现有 Codex responses transport 文本匹配（`unexpected EOF` 或精确 proxy CONNECT `407 Proxy Authentication Required` 路径）；
- 缺少稳定 auth 文件/provider/identity 的事件进入 `ignored`，保留原因，不产生 CPA 写入。

## 5. SQLite 数据模型

迁移只增加新表、索引、cursor 和 enabled interval，不重写或删除现有用量表。

### 5.1 `server_error_priority_demotion_jobs`

建议字段：

| 字段 | 说明 |
|---|---|
| `id` | 自增任务序号；也是同账号 FIFO 的稳定排序键 |
| `event_hash` | `usage_events.event_hash`，唯一约束，保证一个事件一个任务 |
| `source_event_id` | 原始 `usage_events.id`，用于 reconciler 与审计 |
| `credential_key` | 规范化的 provider + auth 文件名 + auth index，用于同账号串行 |
| `file_name` / `auth_index` / `provider` | 当前 CPA 身份定位所需的非秘密字段 |
| `account_snapshot` / `account_id_snapshot` | 现有事件快照，供 identity 校验；不保存 token |
| `status_code` | 触发事件 HTTP 状态 |
| `qualification_code` | `http_502`、`http_503`、`rate_limit_429`、`codex_transport_500` |
| `event_at_ms` | 事件时间 |
| `state` | `pending`、`leased`、`retry_wait`、`completed`、`failed`、`ignored` |
| `available_at_ms` | 下一次可领取时间 |
| `lease_token` / `lease_until_ms` | worker 租约；只允许持有该 token 的 worker 更新 |
| `attempt_count` | 尝试次数 |
| `priority_before` / `priority_target` | 外部写入前后预期值，用于崩溃恢复去重 |
| `last_error_code` / `last_error` | 有界、脱敏的最近错误 |
| `completed_reason` | `demoted`、`floor_reached`、`already_at_target` 等 |
| `created_at_ms` / `updated_at_ms` / `completed_at_ms` | 生命周期时间戳 |

新表不保存 `BaseURL`、management key、代理认证、access token 或完整原始 JSON；运行时仍通过已有 setup resolver 读取当前 CPA 内部配置。

索引：

- `(state, available_at_ms, id)`：领取扫描；
- `(credential_key, id)`：FIFO 前置任务判断；
- `(completed_at_ms, state)`：14 天完成任务清理；
- `event_hash unique`：幂等入队。

### 5.2 `server_error_priority_demotion_cursor`

单行表（`id = 1`）：

- `source_floor_event_id`：首次启用本机制前的历史边界，永不回退；
- `scan_cursor_event_id`：reconciler 已检查到的原始事件 ID；只会向前推进；
- `updated_at_ms`。

首次 bootstrap 的 floor 在 listener/collector 开始接收新事件前建立。迁移和 bootstrap 均使用 `INSERT OR IGNORE`/条件更新；重复启动不会把 cursor 倒退，也不会把历史任务重新加入队列。

### 5.3 `server_error_priority_demotion_enabled_intervals`

| 字段 | 说明 |
|---|---|
| `id` | 自增区间序号 |
| `start_event_id` | 开关开启后第一个应接受的原始事件 ID |
| `end_event_id` | 开关关闭时的最后一个应接受 ID；active 区间为 `NULL` |
| `created_at_ms` / `closed_at_ms` | 区间生命周期 |

约束：最多一个 `end_event_id IS NULL` 的 active 区间；`start_event_id` 单调递增。任务仓库以 `source_event_id` 查询该表确认事件是否在 enabled interval。该查询是决定性条件，因此管理面板切换开关与 collector 之间出现毫秒级竞态时，不会由过期 goroutine 状态误把关闭后的事件写入队列。

## 6. 领取、并发与顺序

### 6.1 SQL 领取规则

每次领取在短 SQLite 事务中完成：

1. 选择 `state in (pending, retry_wait)` 且 `available_at_ms <= now` 的最早任务；
2. 排除存在更早同 `credential_key` 且状态为 `pending/retry_wait/leased` 的任务；
3. 以随机 lease token 更新为 `leased`，设置 `lease_until_ms`；
4. 提交后才调用 CPA，避免长时间持有 SQLite 写锁。

因此同一账号严格 FIFO；不同账号可由最多 8 个 worker 同时执行。现有 `MutationCoordinator` 仍是最后一道文件级互斥，手动修改、自动更新、代理替换和优先级降级不会互相覆盖。

### 6.2 worker 与 wake channel

- 默认 8 个 worker；可用 `USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WORKERS` 配置，限制在 1–32；
- wake channel 默认 8,192，仅保存“有工作可做”的通知；通知满时只合并为一次 wake，不记录丢任务；
- 可用 `USAGE_SERVER_ERROR_PRIORITY_DEMOTION_WAKE_BUFFER` 调整 64–32,768；它不是权威容量，也不会拒绝数据库入队；
- worker 空闲时等待 wake 或短 ticker，唤醒后循环领取直到当前没有可执行任务；reconciler 以独立短 ticker 推进 cursor，避免依赖 wake channel 正确性；
- 启动时先把过期 `leased` 租约恢复为 `retry_wait`，再开始领取；优雅退出不删除任务。

### 6.3 peer inventory

保留现有 20 秒 peer inventory TTL 和 45 秒失败冷却。多 worker 下增加同 key 的 in-flight 合并：同一 CPA setup 只有一个请求在刷新 inventory，其余 worker 等待结果或使用缓存，避免 8 路同时拉取全量凭证。

## 7. 外部 CPA 写入的幂等协议

每个任务仍然代表一次独立 `-1`，不按账号合并。

1. 领取后解析当前 setup、验证目标 identity，并获取 `MutationCoordinator` 文件锁。
2. 读取当前 priority 和 peer inventory，按既有策略计算 `priority_target`。
3. 在任务表写入 `priority_before`、`priority_target`、`updated_at_ms`，随后调用 CPA `PatchPriorityTarget`。
4. CPA 成功返回后，以 lease 条件把任务置为 `completed`；记录 `priority_before -> priority_target` 和事件信息。
5. 如果 priority 已经是 0，任务置 `completed/floor_reached`，不写负数。
6. 如果 CPA 调用超时或 CPAM 在第 3、4 步之间崩溃，下一次领取先读取当前 credential：
   - 当前值等于记录的 `priority_target`：认为外部写入已经发生，只完成任务，不再重复 `-1`；
   - 当前值等于 `priority_before`：重新执行同一次 patch；
   - 当前值为其他值：视为外部并发修改，重新以当前值计算一次该事件的目标并更新预期字段，再执行一次；不静默丢弃。

暂时性网络/CPA 失败进入指数退避 `retry_wait`；永久身份缺失、文件被删除或目标 scope 不支持时进入 `failed/ignored`，保留有界原因和可手动审计记录。任务不会被从数据库静默删除。

## 8. 开关与运行时行为

现有 `USAGE_SERVER_ERROR_PRIORITY_DEMOTION_ENABLED` 和面板互动开关不改名称、不改 API 语义。

- `AutomationRuntime` 在 listener/collector 启动前初始化 queue bootstrap，并在 `Start`、`Reload`、面板开关更新的同步路径中向 worker 调用持久化 `SetEnabled`；HTTP PATCH 在区间事务提交成功后才返回成功；
- disabled：封闭 active interval、推进 cursor 至关闭边界、暂停 reconciler 和领取；已入队任务保留；
- enabled：创建新的 active interval、恢复 reconciler 和领取，先处理持久化 backlog，再接收实时事件；
- 关闭期间产生的失败事件保留在现有 `usage_events` 用于计费/历史，但不属于 enabled interval，重新开启后也不会补作优先级降级；
- 开关关闭不会影响 `usage_events` 写入、计费、quota 或历史统计。

## 9. 错误、重试与可观察性

现有成功降级日志保留，并新增 `job_id`、`attempt` 和 `event_hash`。禁止打印 management key、token、完整代理 URL 或完整 `FailBody`。

周期性（默认 60 秒）输出聚合状态：

```text
[server-error-priority] queue stats pending=... leased=... retry_wait=... completed=... failed=... ignored=... workers=8 oldest_pending_ms=...
```

关键计数：admitted、duplicate、claimed、completed、floor_reached、retried、lease_recovered、failed、ignored。旧的 `job queue full, dropped auth file` 路径删除；任何 SQLite 入队失败必须记录明确错误并由 reconciler 重试，不能伪装成成功。

完成/ignored 任务保留 14 天后清理；`pending/retry_wait/leased/failed` 永不按时间自动删除。清理只触及新表，不触及 `usage_events`、rollup、quota 或计费数据。

## 10. 测试设计

### repository / migration

- 迁移前已有事件不会生成任务；首次 bootstrap 后、开关开启区间内的新事件可被 reconciler 发现；
- 重复 `event_hash` 入队只保留一行；
- cursor 只前进，事务中断后可重试；开关关闭期间的事件不因后续重新开启而回放；
- 14 天清理只删除 `completed/ignored`；
- 迁移前后 `usage_events` 行数、哈希、计费/rollup 查询结果不变。

### worker

- 10 个符合条件事件产生 10 个任务和 10 次独立 patch；
- 同 credential 的 10 个任务按 `source_event_id/id` 串行；不同 credential 的最大并发为配置值（默认 8）；
- wake channel 人为塞满时任务仍全部在 SQLite 中，零 drop；
- lease 过期后可恢复；CPA patch 已成功但 CPAM 在 complete 前崩溃时不会重复降级；
- priority 0 永不写负数；peer rebalance 规则和现有资格判定测试全部保留；
- generic 429、generic 500、非 Codex 500 不入可执行队列。

### 集成与负载

- fake CPA management API 注入 502/503/qualified-429/qualified-500 流量，验证 8 路上限和最终 priority；
- 10,000 条突发失败事件下，任务数与符合条件事件数一致，无 `dropped`；
- `go test ./...`、目标包 `go test -race`，并检查 CPAM 本地 Docker 和服务器二进制的健康状态。

## 11. 部署、回滚与数据安全

1. 在隔离 worktree 构建候选 CPAM；根目录已有 WIP 不得覆盖。
2. 部署前后只核对/重建 CPAM；本地 CPA Core 容器 ID 和服务器 `cpa.service` PID 必须不变。
3. 服务器只更新 `/home/cpam` 的 CPAM 二进制并重启 `cpam.service`；不触碰 `/home/cpa`、`cpa.service`、`cli-proxy-api`。
4. `usage.sqlite` 只执行 additive migration。部署前做 schema/文件状态核对，不复制、覆盖或迁移历史数据库；不改 `data.key`。
5. 回滚到旧 CPAM 二进制时，新表会被旧版本忽略；恢复新版本后任务仍可继续。若需要暂停功能，先关闭现有开关，不删除队列表。

## 12. 完成定义

只有同时满足以下条件才报告完成：

- 持久化任务表、cursor 和 enabled interval 迁移/首次 bootstrap 可重复执行；
- 10 个事件的严格一事件一任务、一任务一成功 patch 通过测试；
- 高并发下无内存队列丢弃，8 路上限和同账号 FIFO 通过测试；
- 崩溃/超时恢复不会重复 `-1`；
- 本地 CPAM 与服务器 CPAM 均部署同一候选版本，CPA Core 身份完全未变；
- 计费、额度、历史统计和既有二改功能回归测试通过。
