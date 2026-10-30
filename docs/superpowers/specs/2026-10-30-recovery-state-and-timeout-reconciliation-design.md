# 凭证恢复状态与写入超时收敛设计

## 目标

修复 CPAM 凭证列表中 Token Recovery 状态偶发不显示、历史成功状态触发重复凭证重载，以及 CPA Core 凭证上传超时后可能已经写入却被直接标记失败的问题。

本次只修改 CPAM Manager Server 与 Web；不修改、重启、构建或部署 CPA Core / `cli-proxy-api`，不改变计费、用量采集、巡检、代理调度、账号优先级或 Token Acquisition 的并发配置。

## 已核实事实

1. 恢复状态不是浏览器本地状态。它持久化在 Manager SQLite 的 `token_recovery_tasks`。
2. `POST /v0/management/token-recovery/query` 对精确 `(file_name, auth_index, account_email)` 身份可稳定返回已完成的自动恢复任务；最近自动成功任务重启后仍可读到 `succeeded` 与 `auto_attempted_at_ms`。
3. 列表中没有 Token Recovery 任务的账号应为空白；普通导入、普通 CPA 文件改动或非 Token Recovery 操作不会伪造“凭证已更新”标签。
4. 当前批量查询在 Handler 内对每个目标单独调用 `Get`。页面刷新、筛选或恢复完成后的凭证重载与该查询相互交错时，会产生不必要的 SQLite 查询与前端状态抖动。
5. 当前 CPA 上传操作在 4 分钟超时时立即以 `cpa_auth_file_timeout` 终态落库。上传请求被客户端取消时，CPA Core 可能已经完成写入；现有流程没有用本次内存中的新 Token 做一次只读回读校验，因此会保守地显示“写入结果待确认”。

## 方案选择

### 方案 A：批量状态快照 + 仅会话内完成刷新 + 上传超时只读复核（采用）

1. 增加只读的 Token Recovery 批量查询路径：一次读取当前页目标的持久化任务，而不是每行单独 SQLite 查询。
2. 查询匹配顺序为：精确身份键 -> 同物理文件与 `auth_index` -> 同物理文件、Provider、规范化邮箱的唯一显示候选。
   - 邮箱兜底仅用于**显示历史状态**；不得用于自动恢复排队、手动恢复排队或任何 CPA 文件写入。
   - 同文件存在多个同邮箱候选时视为歧义并不返回兜底状态，避免把其他记录的状态贴到错误账号。
3. 前端保留页面内已获得的任务状态；首次看到历史 `succeeded` 任务仅显示标签，不触发凭证列表重载。
4. 仅当本浏览器会话先观察到任务 `queued/running`，随后观察到它转为 `succeeded` 时，才对该物理文件发起一次去重后的凭证重载/证据失效操作。
5. CPA 上传返回超时时，不重新获取 Token、不重新上传、不改变 8 路写入门闩。释放写入文件锁后，使用本次内存中的获得结果执行有限次数、短超时的只读下载和 `VerifyMergedAuthJSON`：
   - 成功验证：任务落为 `succeeded`；
   - 仍不可验证：保持现有 `cpa_auth_file_timeout` 与“写入结果待确认”；
   - 绝不把未知结果伪造为成功。

### 方案 B：仅改善前端标签

可减少标签消失，但无法收敛超时上传的未知结果，也保留逐行 SQLite 查询。未采用。

### 方案 C：为所有 CPA 文件操作建立通用变更日志

可给导入、字段编辑等所有操作展示历史，但会扩大持久化模型、语义和 UI 范围。本次不采用。

## 数据流

```text
当前页 <= 50 个 Codex 行
  -> POST /token-recovery/query
  -> 单次 SQLite 批量只读快照
  -> 浏览器按 clientKey 渲染已更新 / 曾自动更新 / 失败原因

任务 queued/running -> succeeded（同一浏览器会话已观察到转变）
  -> 仅刷新对应物理凭证文件的证据

CPA 上传超时
  -> 不重试上传或 Token Acquisition
  -> 短时只读 download + VerifyMergedAuthJSON
  -> verified: succeeded；unconfirmed: cpa_auth_file_timeout
```

## 错误与安全边界

- 响应继续只包含脱敏的 `TokenRecoveryTask`，绝不返回授权 JSON、访问 Token、刷新 Token、ID Token、管理密钥或外部 API 密钥。
- 超时复核仅在上传已经发出且出现 `context.DeadlineExceeded` 时执行；普通 HTTP 上传失败不做隐式重试。
- 状态显示兜底不参与可变操作，且歧义时失败关闭。
- 后台派生开关、计费数据、`usage_events`、SQLite 归档和 CPA 调度均不受影响。

## 验收

1. 对 50 个可见行的恢复状态查询为单个批量仓储读取，响应仍按 `clientKey` 对齐。
2. 精确身份、同 `auth_index`、唯一邮箱显示兜底均可读到正确持久状态；歧义邮箱不返回错误状态。
3. 初次打开含历史 `succeeded` 任务的页面不触发凭证全量重载；页面内 pending -> succeeded 只刷新对应文件一次。
4. 模拟 CPA 上传超时但后续读取已包含新 Token 时，任务最终为 `succeeded`，且 Token Acquisition 与 Upload 各只调用一次。
5. 模拟复核仍失败时，任务保持现有脱敏 `cpa_auth_file_timeout` 终态。
6. 受影响 Go/Vitest 测试、类型检查、生产构建和 `git diff --check` 通过。
