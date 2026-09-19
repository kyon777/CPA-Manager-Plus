import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  CredentialRuntimeMetadataResponse,
  CredentialRuntimeMetadataTarget,
  TokenRecoveryBatchResponse,
} from '@/services/api';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    getCredentialRuntimeMetadata: vi.fn(),
    queryTokenRecoveryBatch: vi.fn(),
  },
}));

vi.mock('@/services/api', () => ({
  usageServiceApi: {
    getCredentialRuntimeMetadata: mocks.getCredentialRuntimeMetadata,
    queryTokenRecoveryBatch: mocks.queryTokenRecoveryBatch,
  },
}));

import {
  useCredentialRuntimeMetadata,
  type UseCredentialRuntimeMetadataResult,
} from './useCredentialRuntimeMetadata';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SCOPE = { apiBase: 'http://manager.local:18317', managementKey: 'manager-key' };
const TARGET_A: CredentialRuntimeMetadataTarget = {
  clientKey: 'row-a',
  fileName: 'a.json',
  authIndex: '7',
  accountEmail: 'a@example.com',
  provider: 'codex',
};
const TARGET_B: CredentialRuntimeMetadataTarget = {
  clientKey: 'row-b',
  fileName: 'b.json',
  authIndex: '8',
  accountEmail: 'b@example.com',
  provider: 'codex',
};

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((nextResolve) => {
    resolve = nextResolve;
  });
  return { promise, resolve };
};

const flush = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

describe('useCredentialRuntimeMetadata', () => {
  let renderer: ReactTestRenderer | null = null;
  let latest: UseCredentialRuntimeMetadataResult | null = null;

  function Harness({
    targets,
    active = true,
  }: {
    targets: CredentialRuntimeMetadataTarget[];
    active?: boolean;
  }) {
    latest = useCredentialRuntimeMetadata({
      active,
      managerRequestScope: SCOPE,
      targets,
    });
    return null;
  }

  beforeEach(() => {
    latest = null;
    mocks.getCredentialRuntimeMetadata.mockReset();
    mocks.queryTokenRecoveryBatch.mockReset();
  });

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
    vi.useRealTimers();
  });

  it('loads metadata for the initial visible page', async () => {
    mocks.getCredentialRuntimeMetadata.mockResolvedValue({
      items: [{ clientKey: 'row-a', proxyUrl: 'http://proxy.example:8080' }],
    } satisfies CredentialRuntimeMetadataResponse);

    await act(async () => {
      renderer = create(<Harness targets={[TARGET_A]} />);
      await flush();
    });

    expect(mocks.getCredentialRuntimeMetadata).toHaveBeenCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      [TARGET_A],
      expect.any(AbortSignal)
    );
    expect(latest?.itemsByClientKey.get('row-a')?.proxyUrl).toBe('http://proxy.example:8080');
    expect(latest?.loading).toBe(false);
  });

  it('does not poll a terminal recovery task', async () => {
    vi.useFakeTimers();
    mocks.getCredentialRuntimeMetadata.mockResolvedValue({
      items: [
        {
          clientKey: 'row-a',
          recoveryTask: {
            id: 1,
            status: 'succeeded',
            fileName: 'a.json',
            provider: 'codex',
            mode: 'manual',
          },
        },
      ],
    } satisfies CredentialRuntimeMetadataResponse);

    await act(async () => {
      renderer = create(<Harness targets={[TARGET_A]} />);
      await flush();
      await vi.advanceTimersByTimeAsync(4_000);
    });

    expect(mocks.queryTokenRecoveryBatch).not.toHaveBeenCalled();
  });

  it('polls every two seconds while a batch task remains pending', async () => {
    vi.useFakeTimers();
    mocks.getCredentialRuntimeMetadata.mockResolvedValue({
      items: [{ clientKey: 'row-a', proxyUrl: 'http://proxy.example:8080' }],
    } satisfies CredentialRuntimeMetadataResponse);
    mocks.queryTokenRecoveryBatch.mockResolvedValue({
      items: [
        {
          clientKey: 'row-a',
          task: {
            id: 2,
            status: 'manual_running',
            fileName: 'a.json',
            provider: 'codex',
            mode: 'manual',
          },
        },
      ],
    } satisfies TokenRecoveryBatchResponse);

    await act(async () => {
      renderer = create(<Harness targets={[TARGET_A]} />);
      await flush();
    });
    act(() => {
      latest?.applyBatchTasks([
        {
          clientKey: 'row-a',
          task: {
            id: 2,
            status: 'manual_running',
            fileName: 'a.json',
            provider: 'codex',
            mode: 'manual',
          },
        },
      ]);
    });
    expect(latest?.itemsByClientKey.get('row-a')?.recoveryTask?.status).toBe('manual_running');

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });

    expect(mocks.queryTokenRecoveryBatch).toHaveBeenCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      [
        {
          clientKey: 'row-a',
          fileName: 'a.json',
          authIndex: '7',
          accountEmail: 'a@example.com',
          provider: 'codex',
        },
      ],
      expect.any(AbortSignal)
    );
  });

  it('ignores an old page response after visible-page targets change', async () => {
    const first = deferred<CredentialRuntimeMetadataResponse>();
    const second = deferred<CredentialRuntimeMetadataResponse>();
    mocks.getCredentialRuntimeMetadata
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise);

    await act(async () => {
      renderer = create(<Harness targets={[TARGET_A]} />);
      await flush();
    });
    await act(async () => {
      renderer?.update(<Harness targets={[TARGET_B]} />);
      await flush();
    });
    expect(mocks.getCredentialRuntimeMetadata).toHaveBeenCalledTimes(2);

    await act(async () => {
      second.resolve({ items: [{ clientKey: 'row-b', proxyUrl: 'http://b.example:8080' }] });
      await flush();
    });
    expect(latest?.itemsByClientKey.get('row-b')?.proxyUrl).toBe('http://b.example:8080');

    await act(async () => {
      first.resolve({ items: [{ clientKey: 'row-a', proxyUrl: 'http://stale.example:8080' }] });
      await flush();
    });
    expect(latest?.itemsByClientKey.has('row-a')).toBe(false);
    expect(latest?.itemsByClientKey.get('row-b')?.proxyUrl).toBe('http://b.example:8080');
  });
});
