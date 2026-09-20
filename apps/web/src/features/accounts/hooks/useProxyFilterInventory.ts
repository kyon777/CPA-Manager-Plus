import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { usageServiceApi, type CredentialRuntimeMetadataTarget } from '@/services/api';
import type { ApiClientRequestScope } from '@/services/api/client';
import { normalizeProxyFilterURLs } from '../model/proxyFilter';

/** CPA Manager currently caps one metadata request at 100 targets. Keep a
 * little headroom for future request-size changes and split large inventories
 * deterministically. */
const TARGET_BATCH_SIZE = 80;

export type UseProxyFilterInventoryOptions = {
  active: boolean;
  managerRequestScope?: ApiClientRequestScope;
  targets: readonly CredentialRuntimeMetadataTarget[];
};

export type UseProxyFilterInventoryResult = {
  proxyURLs: readonly string[];
  enabledCount: number;
  incompleteCount: number;
  loading: boolean;
  error: string;
};

const chunkTargets = <T>(values: readonly T[], size: number): T[][] => {
  const chunks: T[][] = [];
  for (let index = 0; index < values.length; index += size) {
    chunks.push(values.slice(index, index + size));
  }
  return chunks;
};

const errorMessage = (error: unknown): string => {
  if (error instanceof Error && error.message.trim()) return error.message.trim();
  if (typeof error === 'string' && error.trim()) return error.trim();
  return 'Credential proxy metadata unavailable';
};

export const useProxyFilterInventory = ({
  active,
  managerRequestScope,
  targets,
}: UseProxyFilterInventoryOptions): UseProxyFilterInventoryResult => {
  const normalizedTargets = useMemo(
    () =>
      targets.map((target) => ({
        clientKey: target.clientKey.trim(),
        fileName: target.fileName.trim(),
        ...(target.authIndex?.trim() ? { authIndex: target.authIndex.trim() } : {}),
        ...(target.accountEmail?.trim() ? { accountEmail: target.accountEmail.trim() } : {}),
        provider: target.provider.trim().toLowerCase(),
      })),
    [targets]
  );
  const apiBase = managerRequestScope?.apiBase.trim() ?? '';
  const managementKey = managerRequestScope?.managementKey ?? '';
  const enabled = active && Boolean(apiBase && managementKey);
  const [proxyURLs, setProxyURLs] = useState<readonly string[]>([]);
  const [incompleteCount, setIncompleteCount] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const generationRef = useRef(0);
  const abortRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    abortRef.current?.abort();
    abortRef.current = null;
    if (!enabled) {
      setProxyURLs([]);
      setIncompleteCount(0);
      setLoading(false);
      setError('');
      return;
    }

    setLoading(true);
    setError('');
    setIncompleteCount(0);
    const controller = new AbortController();
    abortRef.current = controller;
    try {
      const responses = [];
      for (const batch of chunkTargets(normalizedTargets, TARGET_BATCH_SIZE)) {
        responses.push(
          await usageServiceApi.getCredentialRuntimeMetadata(
            apiBase,
            managementKey,
            batch,
            controller.signal
          )
        );
      }
      if (generationRef.current !== generation) return;

      const itemsByClientKey = new Map<string, { proxyUrl?: string; errorCode?: string }>();
      responses.forEach((response) => {
        (response.items ?? []).forEach((item) => {
          const clientKey = item.clientKey?.trim();
          if (clientKey) itemsByClientKey.set(clientKey, item);
        });
      });

      let incomplete = 0;
      const projectedURLs: string[] = [];
      normalizedTargets.forEach((target) => {
        const item = itemsByClientKey.get(target.clientKey);
        if (!item) {
          incomplete += 1;
          return;
        }
        if (typeof item.proxyUrl === 'string' && item.proxyUrl.trim()) {
          projectedURLs.push(item.proxyUrl);
          return;
        }
        // An error without a projected URL means we cannot know whether this
        // credential consumes one of the requested proxies. Do not silently
        // classify such a URL as unused.
        if (item.errorCode?.trim()) incomplete += 1;
      });
      setProxyURLs(normalizeProxyFilterURLs(projectedURLs));
      setIncompleteCount(incomplete);
    } catch (requestError) {
      if (generationRef.current !== generation || controller.signal.aborted) return;
      setProxyURLs([]);
      setIncompleteCount(normalizedTargets.length);
      setError(errorMessage(requestError));
    } finally {
      if (
        generationRef.current === generation &&
        abortRef.current === controller &&
        !controller.signal.aborted
      ) {
        setLoading(false);
      }
    }
  }, [apiBase, enabled, managementKey, normalizedTargets]);

  useEffect(() => {
    generationRef.current += 1;
    void load();
    return () => {
      generationRef.current += 1;
      abortRef.current?.abort();
      abortRef.current = null;
    };
  }, [load]);

  return {
    proxyURLs,
    enabledCount: normalizedTargets.length,
    incompleteCount,
    loading,
    error,
  };
};
