# 已启用筛选下的正常可用卡片计数设计

## 目标
当凭证列表筛选器选择“已启用”时，顶部“正常可用”卡片显示当前已启用筛选范围的完整数量，而不是当前分页（最多 50 条）中被判定为可用的数量。列表本身继续使用分页。

## 现状与问题
CPAM 的凭证列表已经通过 Manager Server 分页。服务端返回当前查询的 `total`，而前端的 `buildAccountMetrics(rows, ...)` 只对当前页 `rows` 计算健康指标。因此第一页 50 条中有 45 条可用时，卡片显示 45，即使全部已启用账号共有 292 条。

## 方案
仅在以下条件同时满足时覆盖卡片的 `available` 值：

1. 当前状态筛选为 `enabled`（已启用）；
2. 当前使用 Manager Server 分页路径；
3. 分页快照 `sync_status` 已为 `ready`；
4. 服务端返回的 `total` 是非负整数。

覆盖值使用服务端当前查询的 `total`。这样默认“全部/Codex”范围显示全部已启用数量；如果用户另加搜索或提供商筛选，卡片仍准确反映该筛选范围内的已启用数量。暖机阶段不覆盖，避免把临时部分结果显示成最终值。

不修改 CPA Core、额度/计费数据、SQLite、列表分页、可用状态判定，也不加载额外凭证 JSON。

## 代码边界
- `apps/web/src/features/accounts/model/accountRows.ts`：增加一个纯函数，将 ready 的分页 enabled 总数应用到展示指标。
- `apps/web/src/features/accounts/model/accountRows.test.ts`：覆盖 ready 覆盖、warming 不覆盖、非 enabled 不覆盖。
- `apps/web/src/features/accounts/AccountsPage.tsx`：在现有指标计算处调用纯函数，并补齐依赖。

## 验收标准
- 选择“已启用”，第一页即使只有 45/50 条健康账号，快照 ready 后“正常可用”显示 `total`（例如 292）。
- 快照 warming 或分页接口不可用时，不伪造最终数量，保留现有本地指标。
- “可用”筛选仍使用原有严格健康判定。
- 现有分页、最近请求排序、代理投影和计费/用量路径行为不变。
