import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  CredentialRuntimeMetadataResponse,
  CredentialRuntimeMetadataTarget,
} from '@/services/api';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    getCredentialRuntimeMetadata: vi.fn(),
  },
}));

vi.mock('@/services/api', () => ({
  usageServiceApi: {
    getCredentialRuntimeMetadata: mocks.getCredentialRuntimeMetadata,
  },
}));

import {
  useProxyFilterInventory,
  type UseProxyFilterInventoryResult,
} from './useProxyFilterInventory';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SCOPE = { apiBase: 'http://manager.local:18317', managementKey: 'manager-key' };
const TARGETS: CredentialRuntimeMetadataTarget[] = [
  {
    clientKey: 'row-a',
    fileName: 'a.json',
    authIndex: '0',
    accountEmail: 'a@example.com',
    provider: 'codex',
  },
  {
    clientKey: 'row-b',
    fileName: 'b.json',
    authIndex: '1',
    accountEmail: 'b@example.com',
    provider: 'codex',
  },
];

const flush = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

describe('useProxyFilterInventory', () => {
  let renderer: ReactTestRenderer | null = null;
  let latest: UseProxyFilterInventoryResult | null = null;

  function Harness({
    active = true,
    targets = TARGETS,
    projectedProxyURLs,
    projectionComplete = false,
    projectionPending = false,
  }: {
    active?: boolean;
    targets?: CredentialRuntimeMetadataTarget[];
    projectedProxyURLs?: readonly string[];
    projectionComplete?: boolean;
    projectionPending?: boolean;
  }) {
    const result = useProxyFilterInventory({
      active,
      managerRequestScope: SCOPE,
      targets,
      projectedProxyURLs,
      projectionComplete,
      projectionPending,
    });
    useEffect(() => {
      latest = result;
    }, [result]);
    return null;
  }

  beforeEach(() => {
    latest = null;
    mocks.getCredentialRuntimeMetadata.mockReset();
  });

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
  });

  it('loads projected proxy URLs for every enabled target', async () => {
    mocks.getCredentialRuntimeMetadata.mockResolvedValue({
      items: [
        { clientKey: 'row-a', proxyUrl: ' http://used:8080/ ' },
        { clientKey: 'row-b', proxyUrl: 'http://other:8080' },
      ],
    } satisfies CredentialRuntimeMetadataResponse);

    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });

    expect(mocks.getCredentialRuntimeMetadata).toHaveBeenCalledWith(
      SCOPE.apiBase,
      SCOPE.managementKey,
      TARGETS,
      expect.any(AbortSignal)
    );
    expect(latest?.proxyURLs).toEqual(['http://used:8080', 'http://other:8080']);
    expect(latest?.enabledCount).toBe(2);
    expect(latest?.loading).toBe(false);
    expect(latest?.error).toBe('');
  });

  it('uses the CPA list projection without downloading runtime metadata', async () => {
    await act(async () => {
      renderer = create(
        <Harness
          projectedProxyURLs={[' http://used:8080/ ', 'http://other:8080']}
          projectionComplete
        />
      );
      await flush();
    });

    expect(mocks.getCredentialRuntimeMetadata).not.toHaveBeenCalled();
    expect(latest?.proxyURLs).toEqual(['http://used:8080', 'http://other:8080']);
    expect(latest?.incompleteCount).toBe(0);
    expect(latest?.loading).toBe(false);
  });

  it('waits for a warming CPA projection instead of scanning runtime metadata', async () => {
    await act(async () => {
      renderer = create(
        <Harness
          projectedProxyURLs={['http://known:8080']}
          projectionPending
        />
      );
      await flush();
    });

    expect(mocks.getCredentialRuntimeMetadata).not.toHaveBeenCalled();
    expect(latest?.loading).toBe(true);
    expect(latest?.incompleteCount).toBe(TARGETS.length);
  });

  it('marks an omitted target as incomplete instead of silently returning false matches', async () => {
    mocks.getCredentialRuntimeMetadata.mockResolvedValue({
      items: [{ clientKey: 'row-a', proxyUrl: 'http://used:8080' }],
    } satisfies CredentialRuntimeMetadataResponse);

    await act(async () => {
      renderer = create(<Harness />);
      await flush();
    });

    expect(latest?.incompleteCount).toBe(1);
    expect(latest?.error).toBe('');
  });
});
