# 独立派生 SQLite 存储设计

**状态：** LO 已选择方案 2；本文是已确认的设计基线，尚未部署实现。  
**日期：** 2026-10-30  
**范围：** 仅 CPA Manager Plus（CPAM）。

## 目标

将高写入量、可重建的用量派生结果从主 `usage.sqlite` 移到独立的
`usage-derived.sqlite`，使后台回填不再占用主库的 SQLite 写锁，同时保持：

1. 原始 `usage_events`、计费事实、Credits、额度、归档元数据和所有凭证/恢复状态仍只在主库；
2. 凭证列表、当前页 `account-history`、仪表盘和用量维护优先读取派生结果，并只读取主库中派生水位线之后的新尾部；
3. 任何派生故障只降低报表加速能力，绝不改变计费事实或阻断 CPA Core；
4. 现有二改、CPA Core、`cli-proxy-api`、`/home/cpa` 与 `cpa.service` 完全不动。

## 非目标

- 不迁移、重建、覆盖、压缩或删除现有 `/data/usage.sqlite`。
- 不复制认证 JSON、Token、管理 Key 或 CPA Core 数据到派生库。
- 不使用 SQLite `ATTACH` 把主库和派生库放进一个跨库写事务。
- 不允许浏览器请求触发全量重算、全量历史扫描或主库派生写入。
- 不把 `usage_event_identity_ledger`、`usage_archive_*`、`model_prices`、`settings`、
  `token_recovery_tasks`、优先级队列、代理任务、巡检状态等搬离主库；它们不是可随意丢弃的纯派生缓存。

## 数据分层

```text
usage.sqlite（主库、唯一事实源）
  - usage_events / dead_letter_events
  - usage_event_identity_ledger
  - model_prices / 设置 / Credits / 额度 / 归档元数据
  - 恢复、巡检、代理、优先级和其他 CPAM 控制面状态

usage-derived.sqlite（可重建、独立写锁）
  - account-history / dashboard hourly / pricing / monitoring 的物化行
  - 这些物化行对应的 checkpoint、coverage、generation、price revision
  - 派生库专用的 applied-event ledger、索引和维护状态
```

`usage_event_identity_ledger` 必须留在主库，因为采集去重、归档和删除安全会依赖它。
派生库使用自己的 `derived_applied_events`（或等价按 stream/generation 的水位线）避免重复累计；
它不能复用或写入主库的 identity ledger。

## 配置与启停语义

新增以下 CPAM 环境变量：

```text
USAGE_DERIVED_STORAGE_MODE=primary|separate      # 默认 primary，保持旧版行为
USAGE_DERIVED_DB_PATH=/data/usage-derived.sqlite # separate 时默认 DataDir/usage-derived.sqlite
USAGE_DERIVED_SOURCE_BATCH_SIZE=500              # 限制一次主库只读批次
USAGE_DERIVED_CYCLE_MAX_BATCHES=2                # 单次周期最多读取/写入的批数
USAGE_DERIVED_YIELD_MS=100                       # 周期之间主动让出 CPU / I/O
```

现有 `USAGE_BACKGROUND_DERIVATIONS_ENABLED` 保留为**是否运行 Worker**的唯一开关：

| 模式 | `USAGE_BACKGROUND_DERIVATIONS_ENABLED` | 行为 |
| --- | --- | --- |
| `primary` | `true` | 现有单库派生行为，兼容旧部署。 |
| `primary` | `false` | 停止旧 Worker，前端仍可读主库已有派生表。 |
| `separate` | `true` | 启动独立派生 Worker；主库只读，派生库独立写入。 |
| `separate` | `false` | 停止独立 Worker；前端仍可读取已存在的独立派生快照。 |

`separate` 模式绝不启动旧的主库派生 Worker、主库 `StartDerivedMaintenance` 或旧派生清理。
配置启动时必须拒绝“独立模式却仍启动旧主库派生 Worker”的组合，防止双路径同时累计。

## 两库读写协议

### 写入协议

每个 stream（`account-history`、`pricing`、`dashboard-hourly`、`monitoring-projection`、
`monitoring-metadata`、`monitoring-stats`、Codex legacy identity evidence）按以下顺序运行：

1. 读取派生库的 `(source_epoch, generation, coverage_event_id)`；
2. 用**主库只读连接**获取一个短暂的 `target_event_id`，并读取 `(coverage_event_id, target_event_id]`
   内最多 `USAGE_DERIVED_SOURCE_BATCH_SIZE` 条事件；
3. 结束主库只读事务；不得把主库读事务带入任何派生库写入；
4. 在派生库一个短写事务内写入聚合行、派生库本地去重账本和新水位线；
5. 提交派生库事务后才发布新的 coverage；失败时 coverage 不前进。

主库源连接必须以 SQLite `mode=ro` + `query_only=ON` 打开，且不运行 `Migrate`、不带
`_txlock=immediate`。它的 API 只暴露查询能力，编译期不提供 `Exec`/写事务入口。

独立派生 DB 的 writer 可以使用 WAL，但只会竞争 `usage-derived.sqlite` 的写锁。
每轮最多两个小批次，随后按配置 yield；源读取耗时过长、主库出现 `BUSY`、或请求延迟监测恶化时，
Worker 必须指数退避并降低批量，绝不长时间持有主库快照。

### 读取协议

前端/API 的读取不依赖 Worker 是否开启：

```text
派生库已验证的 generation / coverage
        +
主库 (coverage_event_id, request_target_event_id] 的小尾部
        =
账户历史、价格、仪表盘结果
```

读取先取派生库稳定快照，再在主库建立一个短只读快照读取尾部。两者不做跨库事务；
响应中只承诺该主库 `target_event_id` 时刻的结果。新到达事件自然由下一次刷新显示。

价格只在以下条件同时成立时标记为 `fresh`：源 epoch 一致、价格 revision 一致、派生 coverage
完整、归档边界可证明。价格 revision 不一致时可显示最后一次已验证金额，但必须标记
`stale_repricing`；绝不把旧价格伪装成当前精确价格，也不把已知金额清为零。

普通 `account-history` 请求在独立模式下只能读取或唤醒 Worker；即使携带旧的 `catch_up=true`，
也不得在 HTTP 请求内同步写入主库或跑全量派生。

## 代次、价格与归档安全

派生库保存独立的 `derived_store_meta` / `derived_stream_state`：

```text
source_epoch
stream_name
generation
status                 # rebuilding | ready | failed | paused
coverage_event_id
target_event_id
pricing_revision
archive_generation
last_success_at_ms
last_error_code         # 不保存 Token、请求正文或代理凭据
```

- 主库新增一次性的 `usage_source_identity` 记录（随机 source epoch）；它只用于发现错误指向、
  错库或恢复后的派生库，不能包含密钥。
- 价格模型变更创建派生库中的新 pricing generation；旧 generation 可继续以
  `stale_repricing` 形式服务，只有新 generation 覆盖完成才原子切换 active generation。
- 归档删除前必须先从派生库读取所有必须 stream 的 coverage；只有覆盖到待删 archive segment 的
  最大 raw event ID，且 source epoch/archive generation 一致，才允许删除主库原始事件。
- 若历史事件已归档，首次独立库回填必须通过既有 archive reader 读取 archive segment，或通过
  已验证的 legacy materialization bootstrap 导入。两者均不可证明完整时，stream 保持不就绪，
  不允许新的原始事件删除。

## 兼容、故障与回滚

1. 初次切换不会删除主库中已有派生表；它们只作为只读兼容回退保留一个发布周期。
2. 独立派生库首次创建失败时，CPAM 主服务仍启动，原始采集/计费继续；状态端点标记
   `separate_derived=degraded`，不偷偷回退为主库后台写入。
3. 独立库读取失败、source epoch 不匹配、coverage 不完整或 archive proof 失败时，读路径
   只能使用已验证 legacy 行、受限原始尾部或 `pending`，不能编造成本。
4. 回滚只需将 `USAGE_DERIVED_STORAGE_MODE=primary` 并保持
   `USAGE_BACKGROUND_DERIVATIONS_ENABLED=false` 后重启 **cpam.service**；主库和 CPA Core 不需要恢复。
5. `usage-derived.sqlite` 可重建，但部署仍保留其文件与 WAL/SHM，不在运行中手工复制、truncate、
   move 或覆盖它。

## 可观测性与验收

`/status?scope=database-maintenance` 和用量维护状态增加脱敏的独立派生字段：

- storage mode、worker enabled、source epoch 是否匹配；
- 每个 stream 的 coverage、target、lag、generation、状态和最近成功时间；
- 独立库文件/WAL 大小、最近批次耗时、退避次数；
- 不返回数据库绝对路径、Token、管理 Key、认证 JSON、代理 URL、原始错误正文。

验收必须证明：

- 主库 `usage_events`、Credits、配额、归档、恢复、优先级队列的行为与切换前一致；
- 独立 Worker 执行时主库不出现派生表写入，前端当前页的 `account-history` p95 不被后台回填拖慢；
- 账户页、仪表盘和用量维护在覆盖完整后与旧精确结果一致；
- `cpa.service` / CPA Core 容器身份在本地和服务器部署前后不变。