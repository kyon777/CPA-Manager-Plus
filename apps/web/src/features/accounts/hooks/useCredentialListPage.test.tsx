import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CredentialListResponse } from '@/services/api';

const { mocks } = vi.hoisted(() => ({
  mocks: { list: vi.fn() },
}));

vi.mock('@/services/api', () => ({
  credentialListApi: { list: mocks.list },
}));

import { useCredentialListPage } from './useCredentialListPage';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SCOPE = { apiBase: 'http://manager.local:18317', managementKey: 'manager-key' };
const RESPONSE: CredentialListResponse = {
  files: [{ name: 'a.json', email: 'a@example.com', proxy_url: 'http://proxy:1' }],
  total: 101,
  page: 2,
  page_size: 50,
  has_more: true,
  proxy_url_included: true,
  sync_status: 'ready',
};

const flush = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

describe('useCredentialListPage', () => {
  let renderer: ReactTestRenderer | null = null;
  let latest: ReturnType<typeof useCredentialListPage> | null = null;

  function Harness() {
    const result = useCredentialListPage({
      active: true,
      managerRequestScope: SCOPE,
      request: { page: 2, page_size: 50, search: 'a', include_proxy_url: 1 },
    });
    useEffect(() => {
      latest = result;
    }, [result]);
    return null;
  }

  function DisabledHarness() {
    const result = useCredentialListPage({
      active: false,
      managerRequestScope: SCOPE,
      request: { page: 1, page_size: 50, include_proxy_url: 1 },
    });
    useEffect(() => {
      latest = result;
    }, [result]);
    return null;
  }

  beforeEach(() => {
    latest = null;
    mocks.list.mockReset();
    mocks.list.mockResolvedValue(RESPONSE);
  });

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
    vi.useRealTimers();
  });

  it('loads the current page through manager server and exposes metadata', async () => {
    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });
    expect(mocks.list).toHaveBeenCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      { page: 2, page_size: 50, search: 'a', include_proxy_url: 1 },
      expect.any(AbortSignal)
    );
    expect(latest?.data).toEqual(RESPONSE);
    expect(latest?.loading).toBe(false);
    expect(latest?.error).toBe('');
  });

  it('stays idle while the server projection is not eligible', async () => {
    await act(async () => {
      renderer = create(<DisabledHarness />);
      await flush();
    });
    expect(mocks.list).not.toHaveBeenCalled();
    expect(latest?.data).toBeNull();
    expect(latest?.loading).toBe(false);
  });

  it('polls until the server global summary is ready', async () => {
    vi.useFakeTimers();
    mocks.list
      .mockResolvedValueOnce({ ...RESPONSE, global_metrics_status: 'warming' })
      .mockResolvedValueOnce({
        ...RESPONSE,
        global_metrics_status: 'warming',
      })
      .mockResolvedValueOnce({
        ...RESPONSE,
        global_metrics_status: 'ready',
        global_metrics: {
          total: 101,
          enabled: 100,
          available: 90,
          needs_attention: 1,
          quota_risk: 2,
          disabled: 1,
          unconfirmed: 7,
          needs_inspection_action: 0,
        },
      });
    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
      await flush();
    });
    expect(mocks.list).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
      await flush();
    });
    expect(mocks.list).toHaveBeenCalledTimes(3);
    expect(latest?.data?.global_metrics_status).toBe('ready');
  });
});
