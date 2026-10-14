import { describe, expect, it } from 'vitest';
import zhCN from './zh-CN.json';

describe('zh-CN account policy copy', () => {
  it('states complete batch JSON paste success with its full count', () => {
    expect(zhCN.auth_files.paste_success_many).toBe(
      '全部成功：已保存 {{count}}/{{count}} 个认证文件'
    );
  });

  it('describes the qualified priority-demotion policy without corrupted text', () => {
    const policy = zhCN.accountPolicy;
    const fields = [
      policy.serverErrorPriorityDemotion_summary,
      policy.serverErrorPriorityDemotion_description,
      ...policy.serverErrorPriorityDemotion_behavior,
    ];

    expect(fields.some((field) => field.includes('?'))).toBe(false);
    expect(policy.serverErrorPriorityDemotion_description).toBe(
      '此策略只处理新入库的真实请求监控事件。HTTP 429 仅在响应体为 {"detail":"Rate limit exceeded"} 时触发；它会重新确认匹配 JSON 账号，比较同 Provider 的已启用账号优先级后再降级。'
    );
    expect(policy.serverErrorPriorityDemotion_behavior).toEqual([
      '仅处理新入库且唯一的 HTTP 502、503，或响应体为 {"detail":"Rate limit exceeded"} 的 HTTP 429 请求事件；刷新页面或读取历史不会重复降级',
      '重新读取并验证匹配 JSON 账号，再比较同 Provider 的已启用账号；若目标优先级更高，则降到其他账号最高优先级减 1，否则自身减 1',
      '优先级为 0、账号身份不明确或优先级不是整数时跳过，不会修改其他账号',
    ]);
  });
});
