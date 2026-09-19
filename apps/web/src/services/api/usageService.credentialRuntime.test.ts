import axios from 'axios';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => false,
}));

import { usageServiceApi } from './usageService';

const BASE = 'http://manager.local:18317';
const KEY = 'manager-key';
const TARGET = {
  clientKey: 'row-a',
  fileName: 'a.json',
  authIndex: '7',
  accountEmail: 'person@example.com',
  provider: 'codex' as const,
};

let postSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  postSpy = vi.spyOn(axios, 'post').mockResolvedValue({ data: { items: [] } } as never);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('credential runtime manager API', () => {
  it('posts only redacted visible-page locators to the metadata endpoint', async () => {
    const controller = new AbortController();

    await usageServiceApi.getCredentialRuntimeMetadata(BASE, KEY, [TARGET], controller.signal);

    expect(postSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/credential-runtime-metadata',
      { targets: [TARGET] },
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        signal: controller.signal,
        timeout: 30_000,
      })
    );
    expect(JSON.stringify(postSpy.mock.calls[0]?.[1])).not.toContain('access_token');
  });

  it('posts batch manual recovery targets with a client key for row reconciliation', async () => {
    await usageServiceApi.requestTokenRecoveryManualBatch(BASE, KEY, [TARGET]);

    expect(postSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/token-recovery/manual/batch',
      { targets: [TARGET] },
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        timeout: 30_000,
      })
    );
  });

  it('queries batch recovery state using the same redacted target contract', async () => {
    await usageServiceApi.queryTokenRecoveryBatch(BASE, KEY, [TARGET]);

    expect(postSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/token-recovery/query',
      { targets: [TARGET] },
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        timeout: 30_000,
      })
    );
  });
});
