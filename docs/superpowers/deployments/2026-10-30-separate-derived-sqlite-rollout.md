# CPAM 独立派生 SQLite staged rollout

日期：2026-10-30
范围：仅 CPA Manager Plus（CPAM）
不触碰：CPA Core、`cli-proxy-api`、`/home/cpa`、`cpa.service`

## 当前候选边界

- 主事实库仍是 `usage.sqlite`；计费、Credits、额度、Token Recovery、凭证、代理和优先级状态不迁移。
- `usage-derived.sqlite` 只接管 `account-history` 与 `pricing` 两条可重建流；当前页读取使用已验证快照加有限尾部合并。
- `dashboard-hourly`、`monitoring-*` 的独立表已创建，但本候选未把旧转换器接入新库；在 `separate` 模式下这些读取回退到有界/原始事件路径。
- 归档删除在所有必要流覆盖目标前 fail-closed，不能为了释放空间强删 raw event。

## 本地 staged 验证

在干净隔离 worktree 构建 CPAM 候选镜像，不重建 `cli-proxy-api`：

```powershell
$tag = 'cpa-manager-plus:lo-custom-separate-derived-20261030-' + (git rev-parse --short HEAD)
docker build --pull --platform linux/amd64 `
  --build-arg VERSION=$tag `
  --build-arg SOURCE_COMMIT=(git rev-parse --short HEAD) `
  -t $tag -f Dockerfile.manager-server .
docker image inspect -f '{{.Id}}' $tag
```

本地配置先保持暂停：

```text
USAGE_DERIVED_STORAGE_MODE=separate
USAGE_DERIVED_DB_PATH=/data/usage-derived.sqlite
USAGE_DERIVED_SOURCE_BATCH_SIZE=500
USAGE_DERIVED_CYCLE_MAX_BATCHES=2
USAGE_DERIVED_YIELD_MS=100
USAGE_BACKGROUND_DERIVATIONS_ENABLED=false
```

验证 `/status?scope=database-maintenance`：`derivedStorage.mode=separate`，主库旧派生 checkpoint 不前进，CPAM health 200。确认页面仍能看到已知金额并显示 pending/stale，而不是把金额清零。

再只将 `USAGE_BACKGROUND_DERIVATIONS_ENABLED=true` 打开并重启 CPAM，观察两个 stream 的 coverage/target/lag；不要执行全量 HTTP catch-up。

## 服务器 staged 部署

部署前通过 `LACPA-VPS` 保存以下证据：

```bash
systemctl show --property=MainPID --value cpa.service
systemctl is-active cpa.service
systemctl is-active cpam.service
sha256sum /home/cpam/data/data.key
stat -c '%n %s %Y' /home/cpam/data/usage.sqlite
cp -a /home/cpam/config/cpam.env /home/cpam/backups/config/$(date -u +%Y%m%dT%H%M%SZ)-before-separate-derived/cpam.env
```

仅上传已验证的 CPAM 二进制到 `/home/cpam/_incoming/`，原子替换 `/home/cpam/app/cpa-manager-plus`。不得上传 `usage.sqlite`、`data.key`、secrets 或任何 CPA Core 文件。

第一次服务器启动：

```text
USAGE_DERIVED_STORAGE_MODE=separate
USAGE_DERIVED_DB_PATH=/home/cpam/data/usage-derived.sqlite
USAGE_DERIVED_SOURCE_BATCH_SIZE=500
USAGE_DERIVED_CYCLE_MAX_BATCHES=2
USAGE_DERIVED_YIELD_MS=100
USAGE_BACKGROUND_DERIVATIONS_ENABLED=false
```

只执行：

```bash
systemctl restart cpam.service
systemctl is-active cpam.service
curl -fsS http://127.0.0.1:18317/health
systemctl show --property=MainPID --value cpa.service
systemctl is-active cpa.service
```

确认 `cpa.service` 的 PID/启动时间不变，`usage.sqlite` 与 `data.key` 的 hash/大小未被替换，`usage-derived.sqlite` 由 CPAM 在数据目录创建。

在每次服务器升级或回滚前，除主库和 `data.key` 外，必须单独保存派生库及其 SQLite sidecar；不要把派生库混入 CPA Core 或主库传输包：

```bash
cp -a /home/cpam/data/usage-derived.sqlite \
  /home/cpam/backups/data/<timestamp>-separate-derived/usage-derived.sqlite
for suffix in -wal -shm; do
  if [ -f "/home/cpam/data/usage-derived.sqlite${suffix}" ]; then
    cp -a "/home/cpam/data/usage-derived.sqlite${suffix}" \
      "/home/cpam/backups/data/<timestamp>-separate-derived/usage-derived.sqlite${suffix}"
  fi
done
```

若回滚的是 `usage.sqlite`，不能继续盲用一个来自更高 source epoch 的派生库；先将 `USAGE_BACKGROUND_DERIVATIONS_ENABLED=false`，校验 `/status` 中 source epoch，必要时移走 `usage-derived.sqlite*` 让 CPAM 重新 bootstrap。主库、`data.key`、secrets 和 CPA Core 文件始终保持原位。

baseline 通过后，再只改一行：

```text
USAGE_BACKGROUND_DERIVATIONS_ENABLED=true
```

再次只重启 `cpam.service`。观察 `/status?scope=database-maintenance` 与当前页 50 个账号的 account-history/价格；确认主库 `usage_events` 继续增长，主库旧 rollup 不被新 worker 写入。

## 回滚

若发现金额不一致、source epoch 不匹配、derived DB 损坏或延迟异常：

1. 将 `USAGE_BACKGROUND_DERIVATIONS_ENABLED=false`，只重启 `cpam.service`。
2. 若必须恢复旧读路径，再设 `USAGE_DERIVED_STORAGE_MODE=primary`，保持 `USAGE_BACKGROUND_DERIVATIONS_ENABLED=false`，只重启 `cpam.service`。
3. 保留 `usage-derived.sqlite` 供诊断；不删除、不覆盖 `usage.sqlite`，不执行 `cpa.service`/CPA Core 操作。

## 验收记录

- [ ] 本地 CPAM 镜像构建成功，CPA Core 镜像未被重建。
- [ ] Go focused tests、web type-check 与 account presentation tests 通过。
- [ ] 服务器 CPAM health 内外均 200。
- [ ] CPA Core 身份、`cpa.service` PID/启动时间、`data.key`、主库和 OpenResty 配置未变化。
- [ ] `derivedStorage.mode=separate`，account-history/pricing coverage 可追赶。
- [ ] 计费事实与当前页已知金额保持可见；未把 pending/stale 伪装成 fresh。
