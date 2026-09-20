# 优先级降级重平衡设计

## 背景

当前 502、503，以及精确响应 `{"detail":"Rate limit exceeded"}` 的 429 事件，只把触发账号的 priority 减 1。若触发账号为 150、其他同 Provider 账号均为 100，它会变成 149，仍持续拥有最高选择优先级。

## 行为

处理一次符合条件的事件时，Manager Server 在验证目标账号后读取 CPA 当前凭证集合：

- 只比较与目标相同 Provider 的凭证；
- 排除当前目标本身；
- 排除已禁用凭证；
- 缺失 priority 按 CPA 默认值 0 处理；非法 priority 的同组行不作为最高值；
- `peerMax` 为剩余可参与凭证的最高 priority。

下一 priority：

- 当存在 peer 且 `peerMax < current` 时，设置为 `max(0, peerMax - 1)`；
- 否则设置为 `max(0, current - 1)`；
- 没有可比较 peer 时，保持原有 `max(0, current - 1)` 行为。

因此 `150` 对 `100` 集合会直接变为 `99`；相等优先级会变为自身减 1；priority 不会低于 0。

## 一致性与失败处理

目标身份验证、mutation coordinator、Provider 过滤和最终 PATCH 保持现有流程。完整集合读取失败、目标身份变化或 priority 无法解析时，不执行 PATCH，并写入现有 worker 日志。

## 测试

增加 worker 回归测试覆盖：高于 peer 的跳跃降级、peer 高于目标时自身减一、相等值、禁用/跨 Provider 排除、0 下限和原有 502/503/限定 429 识别。
