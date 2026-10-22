import axios from 'axios';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('@/features/demo/demoMode', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/demo/demoMode')>()),
  isDemoMode: () => false,
}));

import { usageServiceApi } from './usageService';

const BASE = 'http://manager.local:18317';
const KEY = 'manager-key';

let postSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  postSpy = vi.spyOn(axios, 'post').mockResolvedValue({
    data: {
      results: [
        {
          fingerprint: 'a'.repeat(64),
          outcome: 'healthy',
          exitIp: '203.0.113.7',
          latencyMs: 31,
        },
      ],
    },
  } as never);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('proxy pool exit-IP verification API', () => {
  it('posts only opaque proxy fingerprints to the manager endpoint', async () => {
    const fingerprints = ['a'.repeat(64), 'b'.repeat(64)];

    await usageServiceApi.verifyProxyPoolExitIPs(BASE, KEY, fingerprints);

    expect(postSpy).toHaveBeenCalledWith(
      'http://manager.local:18317/v0/management/proxy-replacements/proxy-pool/verify-exit-ip',
      { fingerprints },
      expect.objectContaining({
        headers: { Authorization: 'Bearer manager-key' },
        timeout: 240_000,
      })
    );
  });
});
