# Codex 邮箱身份统一与派生桶合并设计

## 目标

将 Codex 用量聚合的账号身份统一为规范化邮箱。空间/Workspace ID 只保留为事件快照信息，不再决定账号是否属于同一聚合桶。这样首批导入时没有空间 ID、自动更新后带空间 ID 的事件会进入同一账号桶。

## 约束

- 不修改 `usage_events` 原始事实，不触碰 CPA Core 的请求计费链路。
- 非 Codex provider 的身份算法保持不变。
- 账号邮箱必须是有效、可规范化的 ASCII email；无法识别邮箱时继续使用原有 file/index 回退键。
- 所有 Go/SQL 派生路径使用同一算法。
- 主库 rollup 和独立 `usage-derived.sqlite` 都必须完成身份版本迁移；旧派生行不能继续作为当前代数据被读取。

## 身份算法

Codex：

```text
normalize(account_snapshot) -> lower(trim ASCII email)
key = usage-account-history:3:codex-member:<hex(codex)>:<hex(email)>
```

空间 ID 缺失、存在、变化都不影响 key。邮箱无效时回退 `file-index`/`file`/`auth-index`。

## 派生迁移

1. 提升 `CodexIdentityRevision`，使主库 account-history/pricing revision 失配。
2. 主库按已有迁移机制清空旧账号 rollup，重新从完整 raw/archive source 聚合；同邮箱事件会在 SQL `GROUP BY account_key` 中合并。
3. 独立派生库 generation 纳入身份 revision。generation 变化时删除旧 generation 的 materialized rows/applied events，再从 source 重放；避免旧 `file-index` 与新 `codex-member` 行长期并存。
4. 读取层只接受当前 generation/current key，不把旧桶与新桶分别返回。
5. 迁移期间继续显示已验证旧数据；状态标记 rebuilding/pending，不伪报 ready。

## 验证

- Go 与 SQL key 结果一致。
- 无空间 ID→有空间 ID、空间 ID 变化、邮箱大小写/空格变化均命中同一 key。
- 不同邮箱保持不同 key。
- 主库 rollup、pricing rollup、独立 derived account-history/pricing 均只产生一个账号身份桶。
- 仪表盘全局聚合与 CPA Core 计费回归不受影响。
