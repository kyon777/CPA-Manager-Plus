import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  usageServiceApi,
  type CredentialRuntimeMetadataItem,
  type CredentialRuntimeMetadataTarget,
  type TokenRecoveryBatchItem,
  type TokenRecoveryBatchTargetRequest,
} from '@/services/api';
import type { ApiClientRequestScope } from '@/services/api/client';
import { isPendingRecoveryStatus } from '../model/credentialRuntimeMetadata';

const RECOVERY_POLL_INTERVAL_MS = 2_000;

export type UseCredentialRuntimeMetadataOptions = {
  active: boolean;
  managerRequestScope?: ApiClientRequestScope;
  targets: readonly CredentialRuntimeMetadataTarget[];
};

export type UseCredentialRuntimeMetadataResult = {
  itemsByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>;
  loading: boolean;
  error: string;
  applyBatchTasks(items: TokenRecoveryBatchItem[]): void;
  refresh(): Promise<void>;
};

const normalizeMetadataTargets = (
  targets: readonly CredentialRuntimeMetadataTarget[]
): CredentialRuntimeMetadataTarget[] =>
  targets.map((target) => ({
    clientKey: target.clientKey.trim(),
    fileName: target.fileName.trim(),
    ...(target.authIndex?.trim() ? { authIndex: target.authIndex.trim() } : {}),
    ...(target.accountEmail?.trim() ? { accountEmail: target.accountEmail.trim() } : {}),
    provider: target.provider.trim().toLowerCase(),
  }));

const targetSignature = (targets: readonly CredentialRuntimeMetadataTarget[]): string =>
  targets
    .map((target) =>
      [
        target.clientKey,
        target.fileName,
        target.authIndex ?? '',
        target.accountEmail ?? '',
        target.provider,
      ].join('\u001f')
    )
    .join('\u001e');

const isAbortError = (error: unknown, signal: AbortSignal): boolean => {
  if (signal.aborted) return true;
  if (!error || typeof error !== 'object') return false;
  const named = error as { name?: unknown; code?: unknown };
  return named.name === 'AbortError' || named.code === 'ERR_CANCELED';
};

const errorMessage = (error: unknown): string => {
  if (error instanceof Error && error.message.trim()) return error.message.trim();
  return typeof error === 'string' && error.trim()
    ? error.trim()
    : 'Credential runtime metadata unavailable';
};

const toItemsByClientKey = (
  items: readonly CredentialRuntimeMetadataItem[]
): Map<string, CredentialRuntimeMetadataItem> => {
  const next = new Map<string, CredentialRuntimeMetadataItem>();
  for (const item of items) {
    const clientKey = item.clientKey?.trim();
    if (clientKey) next.set(clientKey, item);
  }
  return next;
};

const toPendingRecoveryTargets = (
  targets: readonly CredentialRuntimeMetadataTarget[],
  itemsByClientKey: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): TokenRecoveryBatchTargetRequest[] =>
  targets.flatMap((target) => {
    if (target.provider !== 'codex') return [];
    const runtime = itemsByClientKey.get(target.clientKey);
    if (!isPendingRecoveryStatus(runtime?.recoveryTask?.status)) return [];
    return [
      {
        clientKey: target.clientKey,
        fileName: target.fileName,
        ...(target.authIndex ? { authIndex: target.authIndex } : {}),
        ...(target.accountEmail ? { accountEmail: target.accountEmail } : {}),
        provider: 'codex',
      },
    ];
  });

export const useCredentialRuntimeMetadata = ({
  active,
  managerRequestScope,
  targets,
}: UseCredentialRuntimeMetadataOptions): UseCredentialRuntimeMetadataResult => {
  const normalizedTargets = normalizeMetadataTargets(targets);
  const normalizedTargetSignature = targetSignature(normalizedTargets);
  const managerApiBase = managerRequestScope?.apiBase.trim() ?? '';
  const managerManagementKey = managerRequestScope?.managementKey ?? '';
  const enabled =
    active && Boolean(managerApiBase && managerManagementKey && normalizedTargets.length > 0);
  const scopeSignature = [managerApiBase, managerManagementKey, normalizedTargetSignature].join(
    '\u0000'
  );

  const [itemsByClientKey, setItemsByClientKey] = useState(
    (): ReadonlyMap<string, CredentialRuntimeMetadataItem> => new Map()
  );
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const itemsRef = useRef(itemsByClientKey);
  const targetsRef = useRef(normalizedTargets);
  const generationRef = useRef(0);
  const metadataAbortRef = useRef<AbortController | null>(null);

  itemsRef.current = itemsByClientKey;
  targetsRef.current = normalizedTargets;

  const replaceItems = useCallback((next: ReadonlyMap<string, CredentialRuntimeMetadataItem>) => {
    itemsRef.current = next;
    setItemsByClientKey(next);
  }, []);

  const applyBatchTasks = useCallback(
    (batchItems: TokenRecoveryBatchItem[]) => {
      if (batchItems.length === 0) return;
      const next = new Map(itemsRef.current);
      for (const batchItem of batchItems) {
        const clientKey = batchItem.clientKey.trim();
        if (!clientKey) continue;
        const current = next.get(clientKey);
        next.set(clientKey, {
          ...(current ?? { clientKey }),
          recoveryTask: batchItem.task,
          ...(batchItem.errorCode ? { errorCode: batchItem.errorCode } : {}),
        });
      }
      replaceItems(next);
    },
    [replaceItems]
  );

  const loadMetadata = useCallback(async (): Promise<void> => {
    if (!enabled) return;
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    metadataAbortRef.current?.abort();
    const controller = new AbortController();
    metadataAbortRef.current = controller;
    const requestTargets = targetsRef.current;
    setLoading(true);
    setError('');
    try {
      const response = await usageServiceApi.getCredentialRuntimeMetadata(
        managerApiBase,
        managerManagementKey,
        requestTargets,
        controller.signal
      );
      if (generationRef.current !== generation || controller.signal.aborted) return;
      replaceItems(toItemsByClientKey(response.items ?? []));
      setError('');
    } catch (requestError) {
      if (generationRef.current !== generation || isAbortError(requestError, controller.signal))
        return;
      setError(errorMessage(requestError));
    } finally {
      if (generationRef.current === generation && !controller.signal.aborted) {
        setLoading(false);
      }
    }
  }, [enabled, managerApiBase, managerManagementKey, replaceItems, scopeSignature]);

  useEffect(() => {
    generationRef.current += 1;
    metadataAbortRef.current?.abort();
    metadataAbortRef.current = null;
    if (!enabled) {
      replaceItems(new Map());
      setLoading(false);
      setError('');
      return;
    }

    replaceItems(new Map());
    void loadMetadata();
    return () => {
      generationRef.current += 1;
      metadataAbortRef.current?.abort();
      metadataAbortRef.current = null;
    };
  }, [enabled, loadMetadata, replaceItems, scopeSignature]);

  const pendingRecoverySignature = useMemo(
    () => targetSignature(toPendingRecoveryTargets(normalizedTargets, itemsByClientKey)),
    [itemsByClientKey, normalizedTargetSignature, normalizedTargets]
  );

  useEffect(() => {
    if (!enabled || !pendingRecoverySignature) return;
    let disposed = false;
    let controller: AbortController | null = null;
    let timer: ReturnType<typeof globalThis.setTimeout> | null = null;

    const schedule = (): void => {
      if (disposed) return;
      if (toPendingRecoveryTargets(targetsRef.current, itemsRef.current).length === 0) return;
      timer = globalThis.setTimeout(() => {
        timer = null;
        void poll();
      }, RECOVERY_POLL_INTERVAL_MS);
    };

    const poll = async (): Promise<void> => {
      const pendingTargets = toPendingRecoveryTargets(targetsRef.current, itemsRef.current);
      if (disposed || pendingTargets.length === 0) return;
      controller?.abort();
      controller = new AbortController();
      try {
        const response = await usageServiceApi.queryTokenRecoveryBatch(
          managerApiBase,
          managerManagementKey,
          pendingTargets,
          controller.signal
        );
        if (disposed || controller.signal.aborted) return;
        applyBatchTasks(response.items ?? []);
        setError('');
      } catch (requestError) {
        if (!disposed && controller && !isAbortError(requestError, controller.signal)) {
          setError(errorMessage(requestError));
        }
      }
      schedule();
    };

    schedule();
    return () => {
      disposed = true;
      if (timer !== null) globalThis.clearTimeout(timer);
      controller?.abort();
    };
  }, [
    applyBatchTasks,
    enabled,
    managerApiBase,
    managerManagementKey,
    pendingRecoverySignature,
    scopeSignature,
  ]);

  return {
    itemsByClientKey,
    loading,
    error,
    applyBatchTasks,
    refresh: loadMetadata,
  };
};
