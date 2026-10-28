import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ServerErrorPriorityDemotionUpdate } from '@/services/api';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    listServerErrorPriorityDemotionUpdates: vi.fn(),
  },
}));

vi.mock('@/services/api', () => ({
  usageServiceApi: {
    listServerErrorPriorityDemotionUpdates: mocks.listServerErrorPriorityDemotionUpdates,
  },
}));

import { useServerErrorPriorityUpdates } from './useServerErrorPriorityUpdates';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SCOPE = { apiBase: 'http://manager.local:18317', managementKey: 'manager-key' };
const UPDATE: ServerErrorPriorityDemotionUpdate = {
  id: 42,
  fileName: 'account.json',
  authIndex: '7',
  provider: 'codex',
  priorityBefore: 27004,
  priorityTarget: 27003,
  completedAtMs: 2_000_000_000_000,
};

const flush = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

describe('useServerErrorPriorityUpdates', () => {
  let renderer: ReactTestRenderer | null = null;
  let onUpdates: (updates: readonly ServerErrorPriorityDemotionUpdate[]) => void;

  function Harness({
    active = true,
    documentVisible = true,
    hasLoaded = true,
    pollingEnabled = true,
    managerRequestScope = SCOPE,
  }: {
    active?: boolean;
    documentVisible?: boolean;
    hasLoaded?: boolean;
    pollingEnabled?: boolean;
    managerRequestScope?: typeof SCOPE;
  }) {
    useServerErrorPriorityUpdates({
      active,
      documentVisible,
      hasLoaded,
      pollingEnabled,
      managerRequestScope,
      onUpdates,
    });
    useEffect(() => undefined, []);
    return null;
  }

  beforeEach(() => {
    vi.useFakeTimers();
    onUpdates = vi.fn();
    mocks.listServerErrorPriorityDemotionUpdates.mockReset();
  });

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
    vi.useRealTimers();
  });

  it('polls immediately then every five seconds only while the loaded account view is visible', async () => {
    mocks.listServerErrorPriorityDemotionUpdates.mockResolvedValue({
      items: [UPDATE],
      nextCursor: { completedAtMs: 2_000_000_000_000, id: 42 },
    });

    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });

    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenLastCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      expect.objectContaining({ completedAtMs: expect.any(Number), id: 0 }),
      expect.any(AbortSignal)
    );
    expect(onUpdates).toHaveBeenCalledWith([UPDATE]);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(4_999);
      await flush();
    });

    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1);
      await flush();
    });

    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(2);
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenLastCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      { completedAtMs: 2_000_000_000_000, id: 42 },
      expect.any(AbortSignal)
    );
  });

  it('preserves the acknowledged cursor while polling is paused, then resumes from it', async () => {
    mocks.listServerErrorPriorityDemotionUpdates
      .mockResolvedValueOnce({
        items: [UPDATE],
        nextCursor: { completedAtMs: UPDATE.completedAtMs, id: UPDATE.id },
      })
      .mockResolvedValueOnce({
        items: [],
        nextCursor: { completedAtMs: UPDATE.completedAtMs + 1, id: UPDATE.id + 1 },
      });

    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(<Harness pollingEnabled={false} />);
      await flush();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(<Harness pollingEnabled />);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenLastCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      { completedAtMs: UPDATE.completedAtMs, id: UPDATE.id },
      expect.any(AbortSignal)
    );
  });

  it('resets the cursor when the manager scope changes while polling is paused', async () => {
    const replacementScope = {
      apiBase: 'http://manager-b.local:18317',
      managementKey: 'manager-key-b',
    };
    mocks.listServerErrorPriorityDemotionUpdates.mockResolvedValue({
      items: [UPDATE],
      nextCursor: { completedAtMs: UPDATE.completedAtMs, id: UPDATE.id },
    });

    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(
        <Harness pollingEnabled={false} managerRequestScope={replacementScope} />
      );
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(<Harness pollingEnabled managerRequestScope={replacementScope} />);
      await flush();
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenLastCalledWith(
      replacementScope.apiBase,
      replacementScope.managementKey,
      expect.objectContaining({ id: 0 }),
      expect.any(AbortSignal)
    );
    const latestRequest = mocks.listServerErrorPriorityDemotionUpdates.mock.calls[
      mocks.listServerErrorPriorityDemotionUpdates.mock.calls.length - 1
    ]?.[2] as { completedAtMs: number };
    expect(latestRequest.completedAtMs).not.toBe(UPDATE.completedAtMs);
  });

  it('does not poll when the list is inactive, hidden, or not loaded', async () => {
    for (const props of [{ active: false }, { documentVisible: false }, { hasLoaded: false }]) {
      await act(async () => {
        renderer = create(<Harness {...props} />);
        await vi.advanceTimersByTimeAsync(2_000);
        await flush();
      });
      expect(mocks.listServerErrorPriorityDemotionUpdates).not.toHaveBeenCalled();
      renderer?.unmount();
      renderer = null;
    }
  });

  it('keeps only one request in flight and aborts it when the page stops being active', async () => {
    let resolve!: (value: {
      items: ServerErrorPriorityDemotionUpdate[];
      nextCursor: { completedAtMs: number; id: number };
    }) => void;
    const pending = new Promise<{
      items: ServerErrorPriorityDemotionUpdate[];
      nextCursor: { completedAtMs: number; id: number };
    }>((nextResolve) => {
      resolve = nextResolve;
    });
    mocks.listServerErrorPriorityDemotionUpdates.mockReturnValue(pending);

    await act(async () => {
      renderer = create(<Harness />);
      await flush();
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(mocks.listServerErrorPriorityDemotionUpdates).toHaveBeenCalledTimes(1);
    const signal = mocks.listServerErrorPriorityDemotionUpdates.mock.calls[0]?.[3] as AbortSignal;

    await act(async () => {
      renderer?.update(<Harness active={false} />);
      await flush();
    });
    expect(signal.aborted).toBe(true);

    await act(async () => {
      resolve({ items: [UPDATE], nextCursor: { completedAtMs: 2_000_000_000_000, id: 42 } });
      await flush();
    });
    expect(onUpdates).not.toHaveBeenCalled();
  });
});
