# Codex HTTP 401 一次性自动更新设计

**日期：** 2026-09-21  
**目标：** 对已启用的 Codex 凭证，在可确认的 HTTP 401 重登录场景中按开关自动更新一次；无论结果如何，后续自动信号均不得再次更新，人工更新仍可用。

## 范围

- Manager Server 新增持久化策略开关 `codexReauthAutoUpdateEnabled`，默认关闭。
- 自动信号仅接受 Codex、HTTP 401、且现有分类为 `reauth` 的请求监控事件；巡检结果也仅接受 `statusCode=401` 且 `action=reauth`。
- 信号进入队列前，Manager Server 使用 CPA Core 当前 auth-files 运行态重新验证目标身份，并要求 `disabled=false`。
- 任务保存独立 `auto_attempted_at_ms`。该字段一旦写入，不因成功、失败、重启或人工任务覆盖而清除。
- 凭证列表从运行时元数据读取该字段，常驻显示“已自动更新一次”；排队/运行中优先显示“自动更新中”。失败原因仍按当前接口返回内容显示。

## 数据流

1. CPA 请求监控或本地巡检产生 Codex 401 重登录信号。
2. 自动信号门读取 Manager Server 的持久化策略；关闭时不创建任务。
3. 门先读取已有任务：已有 `auto_attempted_at_ms` 时直接返回，不访问 CPA Core，也不重新排队。
4. 首次信号调用 CPA Core 的 auth-files 查询，按物理文件名、auth_index、provider 和可用邮箱快照验证当前身份；仅 `disabled=false` 可继续。
5. Token recovery repository 原子创建/更新自动队列任务；任务被后台 worker 成功 claim、即将开始外部 TokenAcquisition 前，才写入 `auto_attempted_at_ms`。
6. 后台恢复服务读取当前 JSON、保留 note/priority/proxy 等字段、通过已有 TokenAcquisition 流程更新 token，并验证上传结果。
7. 自动成功或失败均保留自动尝试标记。人工“服务器更新”可覆盖当前运行状态以重新执行，但不清除标记。

## 边界与失败行为

- CPA Core 身份不可确认、凭证已禁用、策略关闭、非 401、非 `reauth`：不创建自动任务，不访问 TokenAcquisition。
- 自动任务已执行过：重复事件不进入队列。
- 服务重启时正在运行的自动任务仍转为 `auto_failed_manual_only`，且自动尝试标记保留。
- 手动更新与自动更新共享同一凭证任务记录；人工操作可继续，但不会恢复自动资格。
- 不向浏览器返回 auth JSON、Token 或代理密码；列表只展示已有的脱敏任务状态/错误原因。

## UI

- “账号处理策略”→“认证异常”新增 `401 重登录自动更新一次` 开关。
- 说明明确仅适用：已启用 Codex、真实 HTTP 401、重登录分类；每个凭证只自动一次；失败后使用人工更新。
- 凭证卡片/表格名称区域：
  - 自动排队或运行：`自动更新中`。
  - 完成、失败或之后人工处理：`已自动更新一次`。
  - 失败状态继续显示 `更新失败：<接口原因>`。

## 验证

- Go：策略默认关闭/可持久化、gate 的 401/已启用/重复信号路径、仓库自动标记与人工重试保留标记、巡检 401 过滤。
- Web：策略 view-model 和恢复展示状态。
- 全量相关 Go 测试、web typecheck/lint/相关 Vitest、生产构建、Docker 部署 health 检查。
