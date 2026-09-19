# TokenAcquisition 自动恢复设计

## 目标

当 Codex 凭证被判定为“需要重新登录”时，CPA Manager Plus 在服务器端自动向 TokenAcquisition v1 请求一次新 Token，并原地更新 CPA Core 中的原凭证 JSON。失败后停止自动尝试，改由现有 Codex 重新登录弹窗中的人工按钮触发。

浏览器不接触外部 API Key、Token、凭证 JSON 内容或代理账号密码。

## 范围

- 覆盖三类 Codex `reauth` 信号：
  1. 本机巡检结果；本机页面仅提交不含敏感数据的凭证定位信息给 Manager Server。
  2. 服务端 Codex 巡检结果。
  3. 请求监控捕获的已归类 Codex 认证失效/401 事件。
- 每个凭证失效周期只允许一次后台自动请求。
- 自动请求失败后，后台不再重试；人工按钮可显式再次请求。
- 使用 TokenAcquisition v1 的管理员 API Key，绝不使用 CDK。

不包含：批量人工导入、修改 TokenAcquisition 服务、把 Token 返回给浏览器、替换凭证代理配置。

## 外部 API 合同

- Base URL：`https://401.kyon888.xyz`
- 鉴权：`X-Api-Key`，值从 Manager Server 的运行时 secret 读取。
- 提交：`POST /v1/tokens`
- 轮询：`GET /v1/batches/{batch_id}`，每 2 秒；只在 `done=true` 后处理最终结果。
- 每个任务仅提交一个目标邮箱，避免不同账号间的结果映射歧义。

当原凭证有 HTTP 代理时，提交结构化账号代理：

```json
{
  "accounts": [{"email": "account@example.com", "proxy": "http://user:pass@host:port"}],
  "direct": false
}
```

没有账号级 HTTP 代理时使用：

```json
{
  "accounts": [{"email": "account@example.com"}],
  "direct": true
}
```

外部结果的 `proxy` 不回写到 CPA。

## 触发与状态机

持久化的恢复任务以物理 JSON 文件名和凭证定位器（优先 `auth_index`，其次唯一邮箱记录）作为去重键。

```text
eligible
  -> auto_running
  -> succeeded

eligible
  -> auto_running
  -> auto_failed_manual_only

auto_failed_manual_only
  -> manual_running
  -> succeeded | manual_failed_manual_only
```

- `eligible` 时首次 `reauth` 信号排入一次自动任务。
- `auto_failed_manual_only` 永不因页面刷新、巡检重跑、容器重启或重复 401 自动重试。
- 人工按钮可从失败状态创建一项人工任务；显式人工点击允许再次尝试。
- 成功后记录恢复完成；下一次新的 `reauth` 信号可开启新的自动恢复周期。
- 队列和状态放在 SQLite，不能依赖浏览器状态或进程内存。

## 凭证读取、校验与写回

1. 获取目标物理 JSON 内容到 Manager Server 内存。
2. 在对象 JSON 或数组 JSON 中，以 `auth_index` 定位目标记录；无 `auth_index` 时必须只有一条邮箱匹配记录，否则拒绝为歧义。
3. 提取原记录邮箱和账号级 HTTP 代理。
4. 请求并轮询 TokenAcquisition。
5. 仅校验外部 `result.email` 与原记录邮箱一致（去首尾空白、ASCII 大小写无关）。不比较旧、新 `account_id`。
6. 只在完整获得 `access_token`、`refresh_token`、`id_token` 时写回。
7. 合并原 JSON，并以原物理文件名上传回 CPA Core。
8. 下载同一文件重新验证目标记录的邮箱与已写入 Token。

写入字段：

```text
access_token
refresh_token
id_token
chatgpt_account_id（仅当外部 result 中非空时）
```

若原 JSON 同时存在 `account_id`、`accountId` 或 `chatgptAccountId` 等同义账号 ID 字段，写回时同步为新的 `chatgpt_account_id` 值，避免同一 JSON 内的账号 ID 证据相互冲突。该步骤是写入一致性处理，不是旧/新 account ID 的校验条件。

原样保留：

```text
email / account / note / priority / weight / proxy_url / headers / base_url
disabled / models / websockets / excluded-models / 其他未知字段
```

## 并发与失败处理

- 不在等待外部批次期间持有文件锁：先在短锁内读取目标邮箱与当前 HTTP 代理，释放锁后再请求并轮询 TokenAcquisition；拿到结果后重新取得 `AuthFileMutationCoordinator` 的同一物理文件锁，重新下载最新 JSON、定位同一记录、合并、上传并验证。这样既避免长时间阻塞其它凭证操作，也不会用过期 JSON 覆盖并发修改。
- 上传前后的身份定位均以文件名、定位器和邮箱完成；不使用账号 ID 作为接受/拒绝条件。
- 外部 API 返回 409、网络错误、超时、缺失 Token、邮箱不一致、JSON 歧义、CPA 写入或验证失败均视为本次失败。
- 自动失败写入简短脱敏错误码/原因；日志和 SQLite 绝不存储 API Key、Token、完整 JSON 或代理密码。
- CPA 写回失败时原文件保持未改；不会删除旧文件或创建新文件。

## 前端

现有 `CodexReauthDialog` 增加“服务器获取并更新 Token”按钮：

- 点击只调用 Manager Server 的人工恢复端点；不请求 TokenAcquisition，不上传 JSON。
- 显示 `处理中`、`成功`、`失败（可继续人工重试）`。
- 原 OAuth 重登功能保留不变。
- 本机巡检发现 Codex `reauth` 时，前端只发送文件名、运行时 ID、auth index、provider 和邮箱快照到 Manager Server，由后端决定是否首次自动排队。

## 配置与密钥

新增运行时配置：

```text
TOKEN_ACQUISITION_BASE_URL=https://401.kyon888.xyz
TOKEN_ACQUISITION_API_KEY_FILE=/run/secrets/token_acquisition_api_key
```

可兼容 `TOKEN_ACQUISITION_API_KEY` 环境变量，但部署使用文件 secret，且不会把管理员 Key 写入源码、Compose 版本库、浏览器、日志或 SQLite。

## 验证

必须覆盖：

1. 有 `proxy_url` 的账号将该代理随外部请求发送。
2. 无代理账号使用 `direct=true`。
3. 外部邮箱不一致时拒绝写回。
4. 不比较旧、新 account ID；结果含 `chatgpt_account_id` 时正确写回并同步已有同义字段。
5. `note`、`priority`、代理和未知字段在写回后保留。
6. 自动失败后重复 `reauth` 信号不产生第二次外部 POST。
7. 人工按钮可在自动失败后触发一次新任务。
8. 同一凭证并发触发时仅一个任务进入执行。
9. JSON 数组按 `auth_index` 精确更新，不影响兄弟记录。
10. 写回后重新读取验证；验证失败显示失败且不将任务标为成功。
