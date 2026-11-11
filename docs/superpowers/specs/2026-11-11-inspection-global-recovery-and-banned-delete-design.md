# 巡检全量重新登录与确认封禁删除设计

**状态：** 已确认，待实现
**日期：** 2026-11-11
**范围：** CPA Manager Plus（CPAM）本地 Codex 巡检页；不触碰 CPA Core / `cli-proxy-api`。

## 1. 目标

在一次**已完成**的本地 Codex 巡检结果中，给“重新登录”筛选增加两个针对该次
巡检快照的全量操作：

1. `更新全部需重新登录凭证 (N)`：将该快照里全部仍待重新登录的 Codex 凭证，安全地
   提交给既有的 Token Acquisition 人工恢复队列。
2. `删除所有已确认封禁账号`：只删除被持久化恢复任务明确判定为
   `account_banned` 的凭证物理文件；绝不把“需要重新登录”本身当作封禁。

这里的 `N` 是当前已完成巡检结果的 `isReauthAction` 数量，例如截图中的 1239；它不是
当前分页的 20，也不是后来导入、更新或重新分类的全量凭证库存。

## 2. 范围、不变量与非目标

- 仅修改 `CodexInspectionPage` 的本地 CPAM 巡检流；`ServerCodexInspectionPage` 保持
  既有行为，不借此改变服务器巡检或其旧的执行按钮。
- 每次点击都先冻结本次结果快照、连接指纹和严格凭证定位字段。切换 CPAM 连接、开始
  新巡检、或结果失效后，旧操作不得继续写入当前页面状态。
- 只使用既有 Manager API：
  `POST /v0/management/token-recovery/query`、
  `POST /v0/management/token-recovery/manual/batch`、
  `POST /v0/management/auth-files/delete-account-banned-batch`。不新增后端路由，也不改变
  Token Acquisition worker、CPA 写入并发、计费、额度、优先级、派生库或客户端 API。
- 不在进入页面、翻页、切换筛选时扫描全量恢复状态；全量查询只在操作者点击这两个新
  全量按钮后发生。现有当前页 runtime metadata 与当前页按钮保持原有轻量语义。
- 不把 token、原始 JSON、代理 URL 或恢复任务敏感字段写入浏览器存储、通知或日志。
- 删除继续是物理认证文件删除，保留两次危险操作确认；批次提交、浏览器关闭或网络错误
  绝不能降低服务端已有的严格身份、恢复状态和当前 CPA 清单校验。

本地巡检页当前的旧按钮“删除需重新登录账号 (N)”会被隐藏：它按重新登录建议直接删除，
与“只删已确认封禁账号”的新语义冲突。结果面板的通用 prop 可以保留，以免影响服务器
巡检页。

## 3. 界面与交互

仅在“重新登录”筛选被选中、巡检结果已完成且存在有效目标时，工具栏依次保留/显示：

- 现有：`更新当前页凭证 (P)`；
- **新增：** `更新全部需重新登录凭证 (N)`；
- 现有：`删除当前页已封禁账号 (B)`；
- **新增：** `删除所有已确认封禁账号`；
- 现有：复制当前页账号、其它不相关按钮。

全量恢复按钮在预检或提交中显示进度，例如“核验恢复状态 300/1239”或
“已提交 500/900；跳过 339”。全量封禁删除按钮先显示“核验封禁状态 300/1239”，
仅在完整预检成功后弹出两次确认。确认文案同时展示候选账号数和涉及的物理认证文件数，
明确这是不可恢复的物理删除。

任一全量操作运行期间，所有“重新登录”相关的当前页和全量动作都禁用，避免同一凭证被
两个浏览器工作流重复提交或删除；不影响其它纯查看/分页操作。完成、跳过、失败和中断
结果都在工具栏下方使用现有的操作反馈区域展示，并给出有界的失败原因列表。

## 4. 全量重新登录数据流

### 4.1 冻结与预检

1. 点击时从 `result.results.filter(isReauthAction)` 构造严格的
   `TokenRecoveryBatchTargetRequest`；空 locator、非 Codex 或重复 `clientKey` 不进入请求，
   并被计入跳过。
2. 将有效 target 按最多 100 项分段，**顺序**调用既有 `queryTokenRecoveryBatch`。这只读
   Manager 的恢复任务，不会调用 CPA、Token Acquisition 或上游。
3. 所有查询成功后，按当前巡检结束时间使用既有“巡检后人工恢复成功”判断，分类如下：

| 持久化恢复状态 | 全量恢复处理 |
| --- | --- |
| 无任务、普通终态失败、或早于本次巡检的成功任务 | 本次可人工提交 |
| `queued` / `running` | 跳过，已有恢复正在进行 |
| 本次巡检结束后已 `succeeded` | 跳过，已完成恢复 |
| 终态 `account_banned` | 跳过，交由全量封禁删除流程处理 |
| 缺少响应项、定位无效或任务读取异常 | 不提交；记录明确跳过/失败原因 |

任一预检请求出现传输错误或未完成响应时，整个全量恢复**不提交任何尚未提交的 target**，
显示“预检未完成”；这样不会把“状态未知”误当成可以重新入队。

### 4.2 顺序提交与恢复语义

1. 对预检合格 target 以最多 100 项一批，顺序调用
   `requestTokenRecoveryManualBatch`。
2. 一个响应中返回的逐项失败会记录后继续下一批；某一 HTTP/传输请求本身不确定时，停止
   后续批次并保留已确认提交的计数。下一次显式点击会重新预检，`queued`、`running` 和
   本次后已成功项不会被重复提交。
3. 手工批量请求仅持久化恢复任务；实际 Token Acquisition 和 CPA 写入仍由原有 durable
   worker 按既有并发、锁和重试策略执行。页面不会为 1239 个账号新增轮询或等待外部请求。
4. 浏览器关闭只会停止尚未发出的分段；已收到成功响应的分段已经持久化。再次进入或再次
   点击时，预检会恢复幂等行为。

当前页已有的 `useCredentialRuntimeMetadata` 仍只观察可见页，因此其中成功的行可按现有
逻辑立即从旧巡检结果移除。离屏行不进行全量轮询；需要重新巡检才能重新判定整份快照，
避免全量后台读取重新造成页面卡顿。

## 5. 全量确认封禁删除数据流

### 5.1 先核验、再确认

1. 点击时同样冻结全部 `isReauthAction` target，按最多 100 项顺序查询恢复任务。此扫描
   只在点击发生后执行，绝不在首屏或翻页自动进行。
2. 浏览器仅将任务为终态 `account_banned` 且 locator 完整的成员作为候选；任意查询失败、
   缺少响应、非封禁、活动中或身份不完整的成员都不是删除候选。
3. 候选按物理 `fileName` 分组。一个文件在本次冻结的重新登录快照中只要有任一成员未获
   `account_banned` 证明，整组不提交删除；组超过 100 target 时也整组跳过，绝不将共享
   文件拆到两个 Manager 删除请求中。
4. 若没有候选，展示“没有已确认封禁账号”，不弹确认框。若预检任一请求不完整，直接
   中止，不弹确认框，也不删除任何文件。
5. 预检完成后才弹出第一次确认（账号数 + 文件数 + 不可恢复）；保留现有第二次危险确认。
   第二次确认前连接指纹变化会自动取消。

### 5.2 服务端仍是最终裁决者

每个确认后的分组以最多 100 target 顺序调用既有
`deleteAccountBannedAuthFilesBatch`。Manager 服务端继续在每批内：

- 重新读取并严格匹配 durable recovery task；
- 只接受 `tokenrecovery.IsTerminalAccountBanned(task)`；
- 在共享 mutation coordinator 锁内取得一次当前 CPA 凭证清单；
- 验证该物理文件所有当前成员和身份，再以既有 8 路物理文件删除 worker 执行；
- 对 `shared_file_incomplete`、身份不符、文件已不存在、清单变化和删除失败返回逐项结果。

因此浏览器候选只是减少无效调用，不能单独授权删除。任何服务端跳过或失败保持可见并在
反馈中汇总；一个物理文件只会在确认成功后从 `currentAuthFiles`、本地巡检结果和上层凭证
缓存中移除。传输级错误会停止后续分段，已完成删除保持已完成状态；再次点击重新核验后
可以安全继续剩余文件。

## 6. 状态模型与性能边界

复用并扩展现有 `inspectionPageBatchOperation` 的可测试纯函数/类型，增加“snapshot”操作
而不是把数千条状态塞进 React 闭包。该模型至少记录：冻结快照签名、总数、预检进度、
可提交/已提交/已跳过/失败数、有限错误原因、删除 phase 和累计的删除结果。

- 所有浏览器至 Manager 的全量请求最多 100 target，且外层串行；不会向 SQLite、CPA 或
  Token Acquisition 突发投递 1239 个并发写入。
- 删除的外层串行与 Manager 内每批最多 8 个物理文件并发共同限制压力；恢复提交本身不做
  外部 OAuth/CPA 调用。
- 结果面板、翻页和额度/历史用量读取不依赖这些操作；首次进入仍只加载当前页的恢复状态。
- 操作状态只存内存，不写 localStorage。刷新页面后以服务端 durable task / CPA 文件清单
  为真相重新预检。

## 7. 代码边界

预计只需前端私有二改：

- `apps/web/src/features/monitoring/CodexInspectionPage.tsx`：冻结 reauth 快照、分段预检、
  分段恢复/删除、连接世代保护、局部结果 reconciliation，并停止向本地页传入旧的
  `onDeleteReauthPlanned`。
- `apps/web/src/features/monitoring/components/CodexInspectionResultsPanel.tsx`：新增全量按钮、
  disabled/loading/progress/反馈 props；服务器页的通用旧 prop 仍兼容。
- `apps/web/src/features/monitoring/model/inspectionPageBatchOperation.ts`：纯 target 分类、
  100 项分段、物理文件完整分组、累计结果与有界反馈辅助函数。
- 对应 `*.test.ts` / `*.test.tsx` 及四个受支持语言包（`en`、`ru`、`zh-CN`、`zh-TW`）。

不修改 `apps/manager-server`、CPA Core、部署脚本、数据库 schema、计费/派生库或环境变量。
实际完成功能时，将该行为补入 `docs/LO-CPAM-CUSTOM-MEMORY.md`。

## 8. 验收与回归

1. 1239 条重新登录结果可被一次全量恢复操作按 100 项顺序预检/提交；已排队、运行、巡检后
   成功和确认封禁项不会重复提交。
2. 预检错误不会提交未知 target；提交响应中的逐项失败可见；传输中断后再次点击保持幂等。
3. 进入页面、切换分页和点击无关筛选不会产生全量 `token-recovery/query` 请求。
4. 全量删除只在完整预检后出现双确认，且只提交所有冻结成员均确认封禁的物理文件组；组
   不会跨 100 项请求边界拆分。
5. Manager 返回任何不安全结果时，浏览器不移除文件或巡检行；成功删除才同步移除本地缓存。
6. 本地页不再显示“删除需重新登录账号 (N)”这一非封禁全量删除入口；服务器巡检页行为
   不变。
7. `inspectionPageBatchOperation` 单元测试、`CodexInspectionResultsPanel` 组件测试、
   `CodexInspectionPage.lifecycle` 生命周期测试、web type-check/build 和既有 Manager Go
   测试通过；部署必须另行获得明确指令。
