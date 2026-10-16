# 实施计划：全量凭证清单与可见页附加数据

> **执行方式：** 在隔离 worktree 内完成，先写失败测试，再修改生产代码。部署、推送需由用户另行明确要求。

## 1. 清单加载模型

**文件：** `apps/web/src/features/accounts/AccountsPage.tsx`

- 删除 `useCredentialListPage`、`credentialListQuery` 与所有 `serverCredentialList*` 分页状态/分支。
- 令初始载入与工具栏刷新统一调用 `loadFiles()`；保持 `useAuthFilesData` 的 `listWithProxyProjection()`。
- 恢复 `rows -> filter -> sort -> slice` 的全量前端语义，恢复完整 `rows` 的健康指标与 Provider 计数。
- mutation 后继续由 `useAuthFilesData` 刷新全量列表，批量状态改变完成后强制 read-back。

## 2. 当前页附加数据

**文件：**
- `apps/web/src/features/accounts/hooks/useCredentialRuntimeMetadata.ts`
- `apps/web/src/features/accounts/AccountsPage.tsx`
- `apps/web/src/features/accounts/model/accountHistoryRows.ts`

- 为 runtime hook 增加“只查询恢复任务”的批量模式，调用 `token-recovery/query`，不调用 raw-JSON runtime metadata。
- 将全量列表调用配置为 `fetchMetadata: false`、`lookupRecoveryTasks: true`。
- 增加账号历史缓存选择辅助函数；可见页面只请求未缓存目标。连接变更和显式刷新通过既有 revision / scope 失效缓存。
- 保留 current-page `account-window-usage` 请求与其 AbortController；不从首屏自动批量扩展到全部账号。

## 3. 超时预算

**文件：**
- `apps/web/src/utils/constants.ts`
- `apps/web/src/services/api/usageService.ts`
- `apps/manager-server/internal/service/cpaauthfiles/client.go`

- 将 Web API 客户端、usage service 客户端、Manager CPA client 默认超时由 30 秒统一改为 60 秒。

## 4. 测试

- 先让全量加载 wiring、轻量恢复查询、历史缓存选择、60 秒 timeout 测试失败。
- 改动后跑受影响 Vitest、Go package test、`npm run type-check`、`npm run build`、`git diff --check`。
- 不部署、不中断 CPA Core / `cli-proxy-api`。

## 5. 记忆

- 更新 `docs/LO-CPAM-CUSTOM-MEMORY.md`：记录本次恢复的全量清单主路径、可见页附加数据边界、60 秒超时和更新时的保留要求。
