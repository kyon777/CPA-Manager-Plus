# Pricing 派生加速与真实进度设计

**状态：** LO 已明确确认实施本设计。
**日期：** 2026-10-30
**范围：** 仅 CPA Manager Plus（CPAM）。

## 背景与根因

独立派生库的 `account-history` 已从旧主库物化结果 bootstrap 到接近主库水位，
而 `pricing` bootstrap 只能从旧 pricing coverage 开始重放。现有 engine 每轮交替执行一条
history 批次和一条 pricing 批次；在大 backlog 下 pricing 吞吐过低。

更严重的是 sink 把每个已提交小批的 `ToEventID` 当作 stream 的 `target_event_id`。
它不是当前主库 snapshot 的目标水位，所以 `/status?scope=database-maintenance` 会显示
`lagEventId=0`，即使 pricing 实际仍落后数百万 event。pricing 未完成时，separate reader
把 `PricingIncompleteAccountKeys` 错误置为空；monitoring 因而只看到空 pricing 行，当前页的
请求数/Token 也显示 `-`。

## 不变量

- `usage.sqlite`、`usage_events`、Credits、额度、模型价格、归档、Token Recovery、代理、巡检、
  优先级与所有 CPA Core 行为均不变。
- 主库只通过独立 runtime 的 `mode=ro + query_only=ON` 连接读取有界 event 批次；不引入主库
  writer、跨库事务、SQLite ATTACH、HTTP 内同步重算或全量前端读取。
- pricing 金额在未得到完整 pricing coverage 时不得显示为 0 或伪装为精确值。对**当前 HTTP
  请求携带的账号键集合**（页面最多 50 个）必须优先走原有 query-only、按账号键索引的精准
  pricing 回退；coverage 校验完整后立即显示精确金额。仅当该只读回退仍无法证明完整性时，才
  显示请求数、成功率、Token 并将该单个账号金额保留为 `null`。
- 单个 derived writer 仍只写 `usage-derived.sqlite`，不并行开多个 writer 竞争同一 SQLite 写锁。

## 方案比较

### A. 自适应 pricing 快车道（采用）

pricing 真正落后主库达到阈值、generation/epoch/revision 不一致时：保留至多一个
account-history 正常有界批次，然后连续运行 pricing catch-up burst。每个 pricing 批由
独立环境变量设定大小和次数；所有 source 读后立即关闭，随后才写独立库。

优点：先保证当前页 history 保持新鲜，再把剩余 I/O 明确给 pricing；不改变主库写锁边界。
缺点：会提高 CPU、主库只读吞吐和 derived SQLite 写吞吐，因此必须有上限及 yield。

### B. 仅全局调大现有 `USAGE_DERIVED_SOURCE_BATCH_SIZE` / `CYCLE_MAX_BATCHES`

实现最少，但 history 和 pricing 同时变大，无法保证价格 backlog 被优先处理；页面 history
也会争抢更多资源。拒绝。

### C. 前端当前页直接扫描原始事件计算价格

能临时显示金额，但会把每次翻页变成 raw-history 读取，正是此前导致 5,000 账号面板阻塞的路径。
拒绝。

## 设计

### 1. pricing 快车道

新增配置（仅 separate derived runtime 消费）：

```text
USAGE_DERIVED_PRICING_CATCHUP_BATCH_SIZE=2000
USAGE_DERIVED_PRICING_CATCHUP_MAX_BATCHES=8
USAGE_DERIVED_PRICING_CATCHUP_LAG_EVENTS=10000
```

默认值保持保守（`1000 / 4 / 10000`）；服务器用上面的明确值。达到阈值或 pricing generation、
epoch、价格 revision 不一致时，engine 的单轮流程为：

```text
snapshot(main readonly) -> 若 history 落后：history normal bounded batch
                       -> pricing 2,000-event batch × 最多 8
                       -> 25–100ms cooperative yield between pricing batches
                       -> 下一轮重新读取主库 snapshot
```

pricing 已追上或不足阈值时，保留既有 round-robin normal 流程。任意 `BUSY`、source gap、
context cancel 或 derived 写入失败都不推进 coverage；下轮从已提交 watermark 重试。

### 2. pricing 不完整时的当前页精准回退

`loadAccountSnapshot` 先读取独立派生库的 account-history/pricing 物化行并合并受限 tail。若
pricing stream 尚未覆盖 live source snapshot，或其行与已验证 core/history 覆盖不一致，runtime
只针对**本次请求中的账号键**（列表页最多 50 个）在 `sourceDB` 上开启
`ReadOnly + query_only=ON` 事务，复用上游既有 `UsagePricing.LoadAccountRowsTx` 路径。

该路径优先使用按账号键的 monitoring/indexed projection 加原始 tail；它不是全库历史扫描，
不会写 primary，也不会创建 primary writer 或跨库事务。它返回的 pricing 行仍须通过
`accountPricingCoverageIncompleteKeys(coreRows, pricingRows)` 与本次 core/history 行逐账号核验：

1. 覆盖完整：覆盖派生库的 partial pricing 行，当前页立刻返回精确 `total_cost`、Token、请求数。
2. 覆盖不完整：仅对这些未证明的传入账号键调用既有 retained-event 精准补偿；补偿完整则同样返回金额。
3. 仍无法证明：仅这些账号进入 `PricingIncompleteAccountKeys`；monitoring 走已有
   `buildUnpricedAccountHistoryTotals`，保留请求数/Token，金额 `null`、界面显示 `- / 同步中`。

因此，正常的 pricing backlog 不会使当前 50 个账号失去金额显示；不会显示伪零金额；也不会把
浏览器请求扩大为 5,000 账号或全量 raw history 重算。前端不新增 API、不改变计费写入逻辑。

### 3. 真实主库目标状态

每个 `ApplyBatch` 携带 snapshot 的完整 `TargetEventID`，sink 把它与 coverage 分开持久化：

```text
coverage_event_id = 已提交事件批次最后 ID
source_target_event_id = 本轮主库 snapshot 最大 ID
lag = source_target_event_id - coverage_event_id
```

`Store.SeparateDerivedStatus` 每次状态读取还会用主库只读 snapshot 刷新显示目标（避免服务
重启、worker 暂停或旧 state 造成陈旧 target）。公开 handler 若 `coverage < target`，将
状态展示为 `catching_up`，不再显示 `ready + lag=0` 的伪完成。

## 测试与验收

1. engine unit test：pricing backlog 下先给 account-history 至多一条 normal batch，随后运行
   bounded pricing burst；正常状态仍维持 round-robin。
2. sink/store test：partial pricing batch 的 coverage 小于完整 source target，状态 target/lag
   保留主库真实水位。
3. store + monitoring test：history 已完整、pricing 未完整时，当前页有可核验 source pricing 的
   账号立即返回 `matched=true`、请求数/Token/精确 `total_cost`；只有刻意构造的 coverage 不完整
   账号才返回 `total_cost=nil`。证明回退只读取传入账号键，不触发全量读取。
4. status handler test：`/status?scope=database-maintenance` 返回真实 target、正 lag 与
   `catching_up`。
5. 部署后只用有界 50-account `account-history` 请求验证：pricing 未完成时，可核验账号仍立即
   有精确金额；只有无法验证的单个账号金额为 `-`；核对 `/health`、候选二进制 hash、server
   `cpa.service` PID 和本地 CPA Core 容器身份均不变。

## 部署

按 `CPAM_UPDATE_RUNBOOK.md`：从干净隔离 worktree 构建候选，只定向重建本地
`cpa-manager-plus`，再将同一 Linux amd64 二进制原子部署至服务器 `/home/cpam/app/`，
只重启 `cpam.service`。保留 `/home/cpam/config/cpam.env`、`secrets/*`、`data/*`、
OpenResty/证书配置与现有 `usage-derived.sqlite`；不复制、移动、重建或覆盖主库/`data.key`。
