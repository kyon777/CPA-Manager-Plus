# LO 的 CPAM 二改记忆

> 这是 CPAM 二次修改的**规范清单**。执行“更新 CPAM”前必须先阅读本文件；上游代码只能合并进来，不能覆盖下面列出的行为。

更新时间：2026-10-13  
项目：`seakee/CPA-Manager-Plus` 的 LO 私有分支  
个人仓库：`https://github.com/kyon777/CPA-Manager-Plus`  
工作分支：`lo/custom`  
推送规则：只推送 `origin`（个人仓库），`upstream` 只 fetch，禁止 push。

## 1. CPAM 到 CPA Core 的实际连接方式

生产部署文件不在本仓库，而在：

```text
C:\kyon\docker\cpa-manager\compose.yaml
```

Manager 容器的关键环境变量：

```yaml
CPA_UPSTREAM_URL: "http://cli-proxy-api:8317"
```

`cpa-manager-plus` 与 `cli-proxy-api` 都加入外部 Docker 网络 `app-network`，因此 Manager 通过 Docker 内网 DNS 访问 CPA Core，不经过公网 Caddy。当前两者网络名均为 `app-network`。

代码层的连接优先级要分清：

1. `CPA_UPSTREAM_URL` 是 Manager 的环境/兼容启动连接来源。
2. 已保存的 Manager CPA connection（加密 `manager_config_v1`，并保留 legacy setup 镜像）由 `ManagerConfigService.ResolveSetup` 解析，是运行时代理、巡检、批量上传、优先级 worker 使用的实际 `CPAUpstreamURL + ManagementKey`。
3. 因此不要把公网地址或管理 Key 写死到前端、源码、日志或本记忆文件；更换连接必须通过 Manager 的配置流程或受控部署环境变量完成。

同一部署还保留以下性能相关连接参数，更新时不得静默删掉：

```text
USAGE_COLLECTOR_MODE=resp
USAGE_RESP_QUEUE=usage
USAGE_RESP_POP_SIDE=right
USAGE_BATCH_SIZE=100
USAGE_POLL_INTERVAL_MS=500
USAGE_QUERY_LIMIT=50000
```

`TOKEN_ACQUISITION_BASE_URL` 可以保留；API Key 只从 Docker secret 文件读取，禁止进入仓库或浏览器。

## 2. 必须保留的二改功能

### 凭证列表与巡检显示

- 凭证列表和本地/服务端巡检结果长期显示 `备注: <note>`。
- 凭证列表常驻显示 `代理: <proxy_url>`；无代理时不伪造内容。
- 巡检结果显示 Codex `Credits`。
- 月额度为 0 时，Credits 可以让账号继续可用；但 5 小时额度耗尽不能被 Credits 覆盖，7 天额度耗尽可以由可用 Credits 覆盖。
- 400/失败响应合并最新 Header 快照时不能丢失已有 Credits，不能误判成“月额度耗尽”。
- 当前页可复制账号名，一行一个。
- 最近请求排序必须按全局最新请求排序，刷新后新请求仍应排在前面。

### 错误处理与优先级

- 只对 HTTP `502`、`503` 自动降低优先级；不是所有 50x。
- HTTP `429` 只有同时满足响应体 `{"detail":"Rate limit exceeded"}` 才进入限流降优先级路径，普通 429 不触发。
- 降优先级下限为 0。
- 降级时排除当前账号，读取其他账号最高优先级：若最高值低于当前值，目标设为“其他最高值 - 1”；否则当前值减 1；最终 clamp 到 0，避免高优先级账号降一级后仍持续被选中。
- 优先级调整、失败原因和恢复状态必须可追踪，不能只显示笼统的“更新失败”。

### Codex 401 自动恢复与人工恢复

- 只针对“已启用且巡检判定需要重新登录”的账号。
- 开关打开后，每个账号最多自动尝试一次；无论成功或失败都写入一次性标记，后续不自动重复。
- 列表显示“已自动更新一次”或可读失败原因，方便人工核查。
- 自动失败后保留人工重试按钮；按钮操作不应破坏一次性自动尝试语义。
- Token Acquisition 在 Manager Server 内完成：按邮箱匹配目标 JSON，传递 `proxy_url` 给外部接口；只更新返回的 token 字段，保留 `note`、`priority`、`proxy_url`、headers 等原字段；若返回 `chatgpt_account_id`，允许覆盖写入；不要求 account_id 匹配。
- 自动/批量恢复状态、代理元数据和封禁原因通过服务端 runtime metadata 提供；敏感 token 不写入日志、任务状态、浏览器存储或普通 SQLite 记录。

### 封禁账号与代理筛选

- 能识别 `account_banned: 账号已被封禁或停用`，显示中文原因。
- 凭证列表支持删除当前页已封禁账号，数量必须按当前页实际状态统计。
- 凭证列表支持代理 URL 筛选：输入多行代理，自动去除尾部 `/`，只比较“已启用凭证”实际使用的代理；输出未使用代理，一行一个。
- 代理筛选列表保存到 Manager Server，使换浏览器/换电脑仍可继续使用。

### JSON 导入

- 粘贴认证 JSON 默认类型为 `CPA 认证 JSON`。
- 单个 JSON 的文件名自动使用 JSON 的 `email`，不再要求手填名称。
- 数组 JSON 自动拆成多个独立账号文件；多文件上传由 Manager Server 异步串行提交给 CPA Core，避免浏览器并发请求导致 30 秒超时。
- 批量上传成功后失效凭证清单缓存；任务状态只返回文件名、数量和原因，不返回 token。

### API Key / 插件桥接

- 原生 CPA API Key 模型策略桥接必须保留。
- 插件资源中的 API Key 模型策略、限流管理 iframe 和队列控制桥接必须保留。
- 这些桥接只由 CPAM 代理到 CPA Core，不得把 CPA Core 的实现复制或改写到本项目。

### 凭证列表性能优化（2026-10-13）

提交：`05a25d6f perf: reduce credential list load pressure`  
生产镜像：`cpa-manager-plus:lo-custom-v1.14.6-20261013-05a25d6f`

- `/v0/management/auth-files` 使用按 setup 隔离的内存缓存：新鲜 20 秒、 stale-while-revalidate 最长 10 分钟、失败读取冷却 45 秒；写入成功后立即失效。
- 并发冷启动请求合并，避免每个页面组件重复读取 CPA Core。
- 全局“最近请求”排序使用轻量 account recent index；只有当前页拉取完整历史，保留当前页详细状态和全局排序语义。
- 多 JSON 粘贴上传改为单个异步批量作业；上限 100 个文件、总计 32 MB；服务端串行上传并在完成后清理内存字节。
- CPAM 的 502/503/429 优先级 worker 复用凭证清单缓存。
- 以上优化只改 CPAM，不改 `cli-proxy-api`、CPA Core 数据卷或 Core 配置。

## 3. 更新 CPAM 的固定流程

用户说“更新 CPAM”时按以下顺序执行：

1. 先读取本文件和项目根目录 `AGENTS.md`，再读取当前 `lo/custom` 的提交历史。
2. `git fetch upstream`，只查看上游变化；建立备份分支/标签，不在生产 dirty source 上 checkout 或 reset。
3. 将上游合并到隔离工作树的 `lo/custom`，冲突优先保留本文件第 2 节的行为；尤其检查 `AccountsPage`、`usageService`、`proxy/service.go`、`tokenrecovery`、`server_error_priority_demotion`、路由和配置文件。
4. 运行受影响 Go 测试、前端测试、`npm run build`、`git diff --check`；失败时不能部署。
5. 从干净工作树构建 CPAM 专用镜像；生产目录 `C:\kyon\docker\cpa-manager\source` 可能有 CPA/collector 的 dirty 修改，不能用新代码覆盖它。
6. 只修改生产 `compose.yaml` 中 CPAM 镜像 tag/构建元数据，执行：

   ```powershell
   docker compose -f C:\kyon\docker\cpa-manager\compose.yaml up -d --no-build --no-deps cpa-manager-plus
   ```

7. 部署后检查 Manager `/health`、容器健康状态、启动日志，并确认 `cli-proxy-api` 容器 ID 和 `StartedAt` 未改变。
8. 只推送个人仓库：`git push origin lo/custom`；严禁 `git push upstream`。
9. 若增加或改变二改功能，先更新本文件和持久化记忆，再提交代码；不要只依赖聊天记录。

## 4. 明确禁止事项

- 不修改、重启、重建或推送 CPA Core / `cli-proxy-api`。
- 不把管理 Key、Token Acquisition API Key、认证 JSON 或 token 写入 Git、前端、日志或本文件。
- 不在生产 dirty source 上做上游合并。
- 不用上游分支直接覆盖 `lo/custom`，不执行会丢失二改的 `reset --hard`。
- 不把 CPAM 的自定义功能提交给 `seakee/CPA-Manager-Plus` 上游。
