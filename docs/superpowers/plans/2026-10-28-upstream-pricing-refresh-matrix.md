# CPAM 上游价格链路与刷新范围矩阵实施计划

> 范围：仅 CPAM。保留现有 CPA Core、代理、凭证恢复、优先级和巡检二改；不部署、不触碰 CPA Core。

## 目标

1. 账户历史/价格读取恢复到上游 CPAM 的同步精确路径，避免自定义全局价格缓存导致的 `同步中` 长尾和无关账号触发重算。
2. 保留 `240000ms` usage service timeout。
3. 保留最近请求、历史用量、额度窗口三个独立刷新流。
4. 实现已确认的触发范围矩阵：
   - 首次进入列表：全部账号及相关数据。
   - 手动翻页/筛选：当前页。
   - 打开自动刷新：先全部一次。
   - 自动刷新运行中：当前页。
   - 单个账号手动刷新：当前账号。
   - 右上角刷新：全部账号及相关数据。

## 执行顺序

### 1. 后端回归测试（先红后绿）
- 为 `LoadUsagePricingAccountSnapshot` 增加重建期间精确价格回归测试。
- 增加监控服务测试，确认响应不依赖 CPAM 自定义价格缓存队列/`price_state`。
- 先运行测试并记录当前失败。

### 2. 后端恢复上游价格路径
- 仅修改 `store.go` 价格快照读取：使用上游 `LoadAccountRowsTx` 与既有覆盖恢复逻辑。
- 从监控请求关键路径移除自定义 `PriceCache` 读取、`stale_repricing` 排队和后台价格缓存队列依赖。
- 不删除缓存表/迁移文件，保留回滚兼容，但不再启动 `AccountPriceCacheWorker`。
- 不修改 usage events、Credits、计费滚总或 CPA Core。

### 3. 前端刷新范围矩阵
- 保留当前页懒加载与三条独立刷新 revision/controller。
- 给账户历史加载器增加显式 scope：`all`、`current`、`single`。
- 初次进入和右上角刷新使用 `all`；开启自动刷新先使用一次 `all`，后续自动 revision 使用 `current`；分页/筛选使用 `current`；单账号使用 `single`。
- 对同一 scope 的并发请求去重；切换页面/筛选时取消旧的当前页请求，但不把全量刷新误标成当前页刷新。
- `all` 仍覆盖完整 inventory，但先排列当前可见页（及当前显式打开的行），并在每个 200 账号批次完成时立即发布其历史/价格结果；不得等待最后一批才渲染当前页价格。
- 保持账户历史请求并发上限为 2；这只是读取/渲染的调度优化，不新增 writer、重建 worker 或计费数据写入。
- 停止渲染自定义 `价格后台重算中` 状态，回到上游 `sync_status`/精确 `total_cost` 展示。

### 4. 验证
- Go：usagepricing、store、monitoring、worker 相关测试。
- Web：AccountsPage、usageService timeout、账户历史模型测试、typecheck/build。
- 检查 diff 只涉及 CPAM；不部署、不推送上游。

## 不变约束

- `USAGE_SERVICE_TIMEOUT_MS = 240000`。
- 最近请求、历史用量、额度窗口各自独立的刷新周期与取消控制器。
- 原始 usage 数据和计费结果只读；不清理 SQLite 历史数据。
- 本计划完成后再等待用户明确“部署”指令。
