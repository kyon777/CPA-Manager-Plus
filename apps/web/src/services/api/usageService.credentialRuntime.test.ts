import axios from 'axios';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => false,
}));

import { credentialListApi, usageServiceApi } from './usageService';

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
let getSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  postSpy = vi.spyOn(axios, 'post').mockResolvedValue({ data: { items: [] } } as never);
  getSpy = vi.spyOn(axios, 'get').mockResolvedValue({ data: { files: [] } } as never);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('credential runtime manager API', () => {
  it('requests the bounded server-side credential list with proxy projection', async () => {
    await credentialListApi.list(BASE, KEY, {
      page: 3,
      page_size: 50,
      include_proxy_url: 1,
      sort: 'recent_desc',
    });

    expect(getSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/usage-service/credential-list',
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        timeout: 240_000,
        params: {
          page: 3,
          page_size: 50,
          include_proxy_url: 1,
          sort: 'recent_desc',
        },
      })
    );
  });

  it('posts only redacted visible-page locators to the metadata endpoint', async () => {
    const controller = new AbortController();

    await usageServiceApi.getCredentialRuntimeMetadata(BASE, KEY, [TARGET], controller.signal);

    expect(postSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/credential-runtime-metadata',
      { targets: [TARGET] },
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        signal: controller.signal,
        timeout: 240_000,
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
        timeout: 240_000,
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
        timeout: 240_000,
      })
    );
  });

  it('posts only identity locators when deleting terminal account_banned credentials', async () => {
    await usageServiceApi.deleteAccountBannedAuthFilesBatch(BASE, KEY, [
      {
        ...TARGET,
        runtimeId: 'runtime-a',
        accountId: 'account-a',
        accountSnapshot: 'person@example.com',
      },
    ]);

    expect(postSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/auth-files/delete-account-banned-batch',
      {
        targets: [
          {
            ...TARGET,
            runtimeId: 'runtime-a',
            accountId: 'account-a',
            accountSnapshot: 'person@example.com',
          },
        ],
      },
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        timeout: 240_000,
      })
    );
    expect(JSON.stringify(postSpy.mock.calls[0]?.[1])).not.toContain('access_token');
  });

  it('exports selected auth JSON with identity fields only', async () => {
    const target = {
      ...TARGET,
      runtimeId: 'runtime-a',
      accountId: 'account-a',
      accountSnapshot: 'person@example.com',
    };

    await usageServiceApi.exportSelectedAuthFiles(BASE, KEY, [target]);

    expect(postSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/auth-files/export-selected',
      { targets: [target] },
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        timeout: 240_000,
      })
    );
    expect(JSON.stringify(postSpy.mock.calls[0]?.[1])).not.toContain('access_token');
    expect(JSON.stringify(postSpy.mock.calls[0]?.[1])).not.toContain('proxy_url');
  });
});
