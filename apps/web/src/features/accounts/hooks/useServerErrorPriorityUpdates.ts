import { useCallback, useEffect, useMemo, useRef } from 'react';
import {
  usageServiceApi,
  type ServerErrorPriorityDemotionCursor,
  type ServerErrorPriorityDemotionUpdate,
} from '@/services/api';
import type { ApiClientRequestScope } from '@/services/api/client';

const POLL_INTERVAL_MS = 1_000;
const STARTUP_LOOKBACK_MS = 5_000;

export type UseServerErrorPriorityUpdatesOptions = {
  active: boolean;
  documentVisible: boolean;
  hasLoaded: boolean;
  managerRequestScope?: ApiClientRequestScope;
  onUpdates: (updates: readonly ServerErrorPriorityDemotionUpdate[]) => void;
};

const initialCursor = (): ServerErrorPriorityDemotionCursor => ({
  completedAtMs: Math.max(0, Date.now() - STARTUP_LOOKBACK_MS),
  id: 0,
});

const normalizedCursor = (
  value: unknown,
  fallback: ServerErrorPriorityDemotionCursor
): ServerErrorPriorityDemotionCursor => {
  if (!value || typeof value !== 'object') return fallback;
  const record = value as { completedAtMs?: unknown; id?: unknown };
  const completedAtMs = Number(record.completedAtMs);
  const id = Number(record.id);
  if (
    !Number.isFinite(completedAtMs) ||
    !Number.isFinite(id) ||
    completedAtMs < fallback.completedAtMs ||
    (completedAtMs === fallback.completedAtMs && id < fallback.id)
  ) {
    return fallback;
  }
  return { completedAtMs: Math.floor(completedAtMs), id: Math.floor(id) };
};

// useServerErrorPriorityUpdates observes only CPAM's redacted terminal queue.
// It deliberately has no dependency on the credential array, so applying an
// update never creates a new polling session or falls back to a CPA inventory
// fetch.
export const useServerErrorPriorityUpdates = ({
  active,
  documentVisible,
  hasLoaded,
  managerRequestScope,
  onUpdates,
}: UseServerErrorPriorityUpdatesOptions): void => {
  const apiBase = managerRequestScope?.apiBase.trim() ?? '';
  const managementKey = managerRequestScope?.managementKey ?? '';
  const enabled = active && documentVisible && hasLoaded && Boolean(apiBase && managementKey);
  const scopeKey = useMemo(() => `${apiBase}\u0000${managementKey}`, [apiBase, managementKey]);
  const updatesRef = useRef(onUpdates);
  const cursorRef = useRef<ServerErrorPriorityDemotionCursor | null>(null);
  const generationRef = useRef(0);
  const activeRequestRef = useRef<{ generation: number; controller: AbortController } | null>(null);

  updatesRef.current = onUpdates;

  const poll = useCallback(
    async (generation: number): Promise<void> => {
      if (!enabled || generation !== generationRef.current || activeRequestRef.current) return;
      const cursor = cursorRef.current;
      if (!cursor) return;

      const controller = new AbortController();
      activeRequestRef.current = { generation, controller };
      try {
        const response = await usageServiceApi.listServerErrorPriorityDemotionUpdates(
          apiBase,
          managementKey,
          cursor,
          controller.signal
        );
        if (
          controller.signal.aborted ||
          generation !== generationRef.current ||
          activeRequestRef.current?.controller !== controller
        ) {
          return;
        }
        const items = Array.isArray(response.items) ? response.items : [];
        cursorRef.current = normalizedCursor(response.nextCursor, cursor);
        if (items.length > 0) updatesRef.current(items);
      } catch {
        // The next second reuses the last acknowledged cursor. This passive UI
        // projection must not surface an error banner or interfere with the
        // normal credential list/usage request lifecycle.
      } finally {
        if (activeRequestRef.current?.controller === controller) {
          activeRequestRef.current = null;
        }
      }
    },
    [apiBase, enabled, managementKey]
  );

  useEffect(() => {
    generationRef.current += 1;
    const generation = generationRef.current;
    activeRequestRef.current?.controller.abort();
    activeRequestRef.current = null;
    cursorRef.current = enabled ? initialCursor() : null;
    if (!enabled) return;

    void poll(generation);
    const timer = setInterval(() => {
      void poll(generation);
    }, POLL_INTERVAL_MS);
    return () => {
      clearInterval(timer);
      if (generationRef.current === generation) generationRef.current += 1;
      if (activeRequestRef.current?.generation === generation) {
        activeRequestRef.current.controller.abort();
        activeRequestRef.current = null;
      }
    };
  }, [enabled, poll, scopeKey]);
};
