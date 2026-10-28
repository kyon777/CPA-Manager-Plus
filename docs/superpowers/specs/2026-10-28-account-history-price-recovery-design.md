# 凭证列表重建期间逐账号价格恢复设计

## 背景与根因

2026-10-28 的性能修复 `9382c610` 为避免高并发凭证列表在价格索引重建时反复扫描事件历史，将凭证列表的逐账号历史读取从 `LoadAccountRowsTx` 切换为 `LoadAccountRowsForHistoryTx`。

后者在 `pricing_v1` 为 `rebuilding` 或 `clearing` 时只读取当前 revision 已持久化的价格前缀，并刻意跳过事件恢复。Store 随后把缺失价格覆盖的账号标为 unpriced，服务端返回：

```text
total_cost = null
sync_status = pending
```

请求数、Token 和最近请求仍来自核心历史 rollup，因此可见；价格被隐藏。当前服务器上 `pricing_v1` 的重建覆盖远落后于账号历史覆盖，故此行为影响了几乎全部凭证。

这不是 usage event 丢失、CPA 计费失败或模型价格数据丢失；它是 CPAM 展示读路径的有意性能降级。LO 已选择 B：恢复重建期间的旧逐页精确价格读取行为。

## 目标

当凭证列表的当前可见页请求 `account-history` 时，即使 `pricing_v1` 正在 `rebuilding`，也应返回该页账号的精确 `total_cost`，使前端继续展示每个账号的历史金额。

## 设计

### 1. 恢复已有的精确价格读取路径

`Store.LoadUsagePricingAccountSnapshot` 恢复使用既有的 `UsagePricing.LoadAccountRowsTx`：

1. 读取当前 revision 已持久化的逐账号价格行；
2. 对价格覆盖不完整的账号，调用既有 `LoadAccountRowsFromEventsTx`；
3. 该恢复查询使用兼容的 monitoring projection 中按 `account_key` 索引的历史事件，加上 projection watermark 之后的原始 `usage_events` 尾部；
4. 将行与固定的模型价格快照合并，只有覆盖验证完整时返回 `total_cost`。

该路径是只读的，不写入 pricing rollup、usage events、Credits 或 CPA Core。投影不兼容或历史覆盖无法证明完整时继续 fail-closed：该账号的 `total_cost` 保持 `null`，不会显示猜测金额。

### 2. 恢复重建状态下的按页价格覆盖恢复

移除 `rebuilding` 对 `pricingHistoryRecoveryAllowed` 的阻断；`clearing` 仍不允许使用不稳定价格行。重建期间仍按已有的 `accountPricingCoverageIncompleteKeys` 完整性校验逐账号隔离：

- 可恢复账号显示金额；
- 不可恢复账号仍显示 `-`；
- 一个账号无法恢复不能污染同页其他账号；
- `sync_status=pending` 继续表示全局历史 checkpoint 仍在追赶，而非“金额为零”。

前端现有渲染会在 `total_cost != null` 时显示金额；`同步中` 脚注保留为数据索引追赶提示，不再阻止金额显示。

### 3. 性能边界

本方案严格实现已选择的 B：恢复旧行为，不新增 A 方案的缓存、队列或按页限流策略。现有前端的取消过期请求、当前可见页请求和自动刷新节奏保持原样。

由于逐页精确价格会增加 `account-history` 查询成本，验证将测量当前页 50 账号的响应时间；若恢复后再次出现不可接受的卡顿，再另行设计缓存/受控并发方案，不能在本次变更中混入未经确认的行为。

## 非目标与不可变约束

- 不修改 CPA Core、`cli-proxy-api`、`/home/cpa`、`cpa.service`。
- 不修改 usage event 写入、计费数据、Credits、模型价格同步或 SQLite 历史数据。
- 不启用后台全量派生任务，不重建或清理 SQLite。
- 不伪造、复用旧 revision 或估算价格。
- 不改变最近请求、额度窗口、代理、巡检、自动更新和优先级功能。

## 验证标准

1. `pricing_v1=rebuilding` 且当前价格 rollup 缺失时，兼容投影可恢复账号历史的 `total_cost`，并与重建前基线金额一致。
2. 该读取不写入任何 pricing/history/event 表。
3. projection 不兼容时，金额继续 fail-closed 为 `null`，请求数和 Token 仍保持可见。
4. `clearing` 状态不误用不稳定价格行。
5. 既有 `account-history`、pricing coverage、store 与前端相关测试通过。
