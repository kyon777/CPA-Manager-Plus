import axios from 'axios';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => false,
}));

import { monitoringAnalyticsApi } from './usageService';

const BASE = 'http://manager.local:18317';
const KEY = 'manager-key';

let getSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  getSpy = vi.spyOn(axios, 'get').mockResolvedValue({
    data: {
      generated_at_ms: 1,
      window_start_ms: 0,
      active_account_count: 0,
      missing_proxy_account_count: 0,
      proxy_count: 0,
      overloaded_account_count: 0,
      proxies: [],
    },
  } as never);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('proxy overload summary API', () => {
  it('uses one protected aggregate GET and sends refresh only when requested', async () => {
    const controller = new AbortController();

    await monitoringAnalyticsApi.getProxyOverloadSummary(BASE, KEY, false, controller.signal);

    expect(getSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/monitoring/proxy-overload-summary',
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        signal: controller.signal,
        timeout: 240_000,
      })
    );
    expect(getSpy.mock.calls[0]?.[1]).not.toHaveProperty('params');

    await monitoringAnalyticsApi.getProxyOverloadSummary(BASE, KEY, true);
    expect(getSpy).toHaveBeenLastCalledWith(
      'http://manager.local:18317/v0/management/monitoring/proxy-overload-summary',
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        params: { refresh: 1 },
        timeout: 240_000,
      })
    );
  });
});
