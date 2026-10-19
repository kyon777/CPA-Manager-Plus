import axios from 'axios';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => false,
}));

import { usageServiceApi } from './usageService';

const BASE = 'http://manager.local:18317';
const KEY = 'manager-key';

let getSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  getSpy = vi.spyOn(axios, 'get').mockResolvedValue({
    data: {
      items: [],
      nextCursor: { completedAtMs: 1000, id: 10 },
    },
  } as never);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('server-error priority demotion updates API', () => {
  it('uses the manager endpoint, scoped authorization and stable tuple cursor', async () => {
    const controller = new AbortController();

    await usageServiceApi.listServerErrorPriorityDemotionUpdates(
      BASE,
      KEY,
      { completedAtMs: 1000, id: 10 },
      controller.signal
    );

    expect(getSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/server-error-priority-demotions/updates',
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        signal: controller.signal,
        timeout: 240_000,
        params: {
          after_completed_at_ms: 1000,
          after_id: 10,
          limit: 200,
        },
      })
    );
  });
});