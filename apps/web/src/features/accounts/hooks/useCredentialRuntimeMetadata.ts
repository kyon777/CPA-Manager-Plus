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
const EMPTY_SEED_ITEMS: readonly CredentialRuntimeMetadataItem[] = [];

export type UseCredentialRuntimeMetadataOptions = {
  active: boolean;
  managerRequestScope?: ApiClientRequestScope;
  targets: readonly CredentialRuntimeMetadataTarget[];
  /**
   * Redacted task state already carried by the paged credential-list response.
   * Supplying it lets the list render recovery state without downloading each
   * credential's original JSON merely to recover that same state.
   */
  seedItems?: readonly CredentialRuntimeMetadataItem[];
  /**
   * Legacy and diagnostic callers still fetch runtime metadata. Set false when
   * the normal auth-files list already carries proxy_url and raw credential
   * JSON must not be downloaded for list rendering.
   */
  fetchMetadata?: boolean;
  /**
   * Read only redacted token-recovery task rows for the visible page. This is
   * a Manager SQLite query and never asks CPA for the credential JSON.
   */
  lookupRecoveryTasks?: boolean;
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

// Visible rows can be reordered by the live "recent request" sort while
// their membership remains identical. Read lifecycle identity must represent
// that membership, not the transient presentation order, otherwise every
// reorder aborts and restarts the same Manager query.
const targetSetSignature = (targets: readonly CredentialRuntimeMetadataTarget[]): string =>
  [...targets]
    .sort((left, right) => left.clientKey.localeCompare(right.clientKey))
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

const recoveryTaskSignature = (
  task: CredentialRuntimeMetadataItem['recoveryTask'] | undefined
): string =>
  task
    ? [
        task.id,
        task.status,
        task.mode,
        task.updatedAtMs ?? '',
        task.autoAttemptedAtMs ?? '',
        task.lastErrorCode ?? '',
        task.lastErrorMessage ?? '',
      ].join('\u001f')
    : '';

const runtimeMetadataItemSignature = (
  item: CredentialRuntimeMetadataItem | undefined
): string =>
  item
    ? [
        item.clientKey,
        item.proxyUrl ?? '',
        recoveryTaskSignature(item.recoveryTask),
        item.errorCode ?? '',
      ].join('\u001f')
    : '';

const itemSignature = (items: readonly CredentialRuntimeMetadataItem[]): string =>
  items.map(runtimeMetadataItemSignature).join('\u001e');

const sameRuntimeMetadataItems = (
  current: ReadonlyMap<string, CredentialRuntimeMetadataItem>,
  next: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): boolean => {
  if (current.size !== next.size) return false;
  for (const [clientKey, nextItem] of next) {
    const currentItem = current.get(clientKey);
    if (!currentItem || runtimeMetadataItemSignature(currentItem) !== runtimeMetadataItemSignature(nextItem)) {
      return false;
    }
  }
  return true;
};

const retainVisibleItems = (
  current: ReadonlyMap<string, CredentialRuntimeMetadataItem>,
  targets: readonly CredentialRuntimeMetadataTarget[],
  seededItems: ReadonlyMap<string, CredentialRuntimeMetadataItem>
): Map<string, CredentialRuntimeMetadataItem> => {
  const next = new Map<string, CredentialRuntimeMetadataItem>();
  for (const target of targets) {
    const clientKey = target.clientKey.trim();
    if (!clientKey) continue;
    const existing = current.get(clientKey);
    const seeded = seededItems.get(clientKey);
    if (!existing && !seeded) continue;
    next.set(clientKey, {
      ...(existing ?? { clientKey }),
      ...(seeded ?? {}),
      clientKey,
    });
  }
  return next;
};

const toRecoveryLookupTargets = (
  targets: readonly CredentialRuntimeMetadataTarget[]
): TokenRecoveryBatchTargetRequest[] =>
  targets.flatMap((target) => {
    if (target.provider !== 'codex') return [];
    return [
      {
        clientKey: target.clientKey,
        fileName: target.fileName,
        ...(target.authIndex ? { authIndex: target.authIndex } : {}),
        ...(target.accountEmail ? { accountEmail: target.accountEmail } : {}),
        provider: 'codex' as const,
      },
    ];
  });

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
  seedItems = EMPTY_SEED_ITEMS,
  fetchMetadata = true,
  lookupRecoveryTasks = false,
}: UseCredentialRuntimeMetadataOptions): UseCredentialRuntimeMetadataResult => {
  const normalizedTargets = normalizeMetadataTargets(targets);
  const seededItemSignature = itemSignature(seedItems);
  // The signature deliberately governs this identity: parents frequently
  // allocate an equivalent seed array during a render, and recreating this
  // map would otherwise retrigger the metadata initialization effect.
  const seededItemsByClientKey = useMemo(
    () => toItemsByClientKey(seedItems),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- semantic item signature is the cache key.
    [seededItemSignature]
  );
  const managerApiBase = managerRequestScope?.apiBase.trim() ?? '';
  const managerManagementKey = managerRequestScope?.managementKey ?? '';
  const enabled =
    active && Boolean(managerApiBase && managerManagementKey && normalizedTargets.length > 0);
  const metadataFetchEnabled = enabled && fetchMetadata;
  const recoveryLookupEnabled = enabled && lookupRecoveryTasks && !metadataFetchEnabled;
  const scopeSignature = [managerApiBase, managerManagementKey, targetSetSignature(normalizedTargets)].join(
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
  const recoveryLookupGenerationRef = useRef(0);
  const recoveryLookupAbortRef = useRef<AbortController | null>(null);
  // A Manager recovery lookup can still be in flight when a page action
  // creates a new durable recovery task. Keep that newer local task until a
  // lookup started after it can report the authoritative next status.
  const recoveryTaskMutationVersionRef = useRef(0);

  itemsRef.current = itemsByClientKey;
  targetsRef.current = normalizedTargets;

  const replaceItems = useCallback((next: ReadonlyMap<string, CredentialRuntimeMetadataItem>) => {
    if (sameRuntimeMetadataItems(itemsRef.current, next)) return;
    itemsRef.current = next;
    setItemsByClientKey(next);
  }, []);

  const mergeRecoveryLookupTasks = useCallback(
    (batchItems: TokenRecoveryBatchItem[], lookupMutationVersion: number | null) => {
      if (batchItems.length === 0) return;
      const next = new Map(itemsRef.current);
      const keepNewerPendingTasks =
        lookupMutationVersion !== null &&
        recoveryTaskMutationVersionRef.current !== lookupMutationVersion;
      for (const batchItem of batchItems) {
        const clientKey = batchItem.clientKey.trim();
        if (!clientKey) continue;
        const current = next.get(clientKey);
        if (keepNewerPendingTasks && isPendingRecoveryStatus(current?.recoveryTask?.status)) {
          continue;
        }
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

  const applyBatchTasks = useCallback(
    (batchItems: TokenRecoveryBatchItem[]) => {
      recoveryTaskMutationVersionRef.current += 1;
      mergeRecoveryLookupTasks(batchItems, null);
    },
    [mergeRecoveryLookupTasks]
  );

  const loadMetadata = useCallback(async (): Promise<void> => {
    if (!metadataFetchEnabled) return;
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
  }, [managerApiBase, managerManagementKey, metadataFetchEnabled, replaceItems]);

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

    // The normal paged list asks only for compact recovery-task rows. A
    // parent seed refresh may omit a still-visible row while that lookup is
    // in flight, so retain the current visible task instead of briefly
    // clearing its badge. Raw metadata callers keep replacement semantics.
    replaceItems(
      recoveryLookupEnabled
        ? retainVisibleItems(itemsRef.current, targetsRef.current, seededItemsByClientKey)
        : seededItemsByClientKey
    );
    if (!metadataFetchEnabled) {
      setLoading(false);
      setError('');
      return;
    }
    void loadMetadata();
    return () => {
      generationRef.current += 1;
      metadataAbortRef.current?.abort();
      metadataAbortRef.current = null;
    };
  }, [
    enabled,
    loadMetadata,
    metadataFetchEnabled,
    replaceItems,
    recoveryLookupEnabled,
    scopeSignature,
    seededItemSignature,
    seededItemsByClientKey,
  ]);

  useEffect(() => {
    const generation = recoveryLookupGenerationRef.current + 1;
    recoveryLookupGenerationRef.current = generation;
    recoveryLookupAbortRef.current?.abort();
    recoveryLookupAbortRef.current = null;
    if (!recoveryLookupEnabled) return;

    const requestTargets = toRecoveryLookupTargets(targetsRef.current);
    if (requestTargets.length === 0) return;
    const lookupMutationVersion = recoveryTaskMutationVersionRef.current;
    const controller = new AbortController();
    recoveryLookupAbortRef.current = controller;
    setLoading(true);
    setError('');
    void (async () => {
      try {
        // The Manager route accepts at most 100 targets. Current list pages
        // are <= 50, but chunk here so an operator-selected page size cannot
        // silently turn a recovery lookup into a 400 response.
        const items: TokenRecoveryBatchItem[] = [];
        for (let index = 0; index < requestTargets.length; index += 100) {
          const response = await usageServiceApi.queryTokenRecoveryBatch(
            managerApiBase,
            managerManagementKey,
            requestTargets.slice(index, index + 100),
            controller.signal
          );
          items.push(...(response.items ?? []));
        }
        if (recoveryLookupGenerationRef.current !== generation || controller.signal.aborted) {
          return;
        }
        mergeRecoveryLookupTasks(items, lookupMutationVersion);
        setError('');
      } catch (requestError) {
        if (
          recoveryLookupGenerationRef.current !== generation ||
          isAbortError(requestError, controller.signal)
        ) {
          return;
        }
        setError(errorMessage(requestError));
      } finally {
        if (recoveryLookupGenerationRef.current === generation && !controller.signal.aborted) {
          setLoading(false);
        }
      }
    })();

    return () => {
      recoveryLookupGenerationRef.current += 1;
      controller.abort();
      if (recoveryLookupAbortRef.current === controller) {
        recoveryLookupAbortRef.current = null;
      }
    };
  }, [
    mergeRecoveryLookupTasks,
    managerApiBase,
    managerManagementKey,
    recoveryLookupEnabled,
    scopeSignature,
  ]);

  const pendingRecoverySignature = targetSetSignature(
    toPendingRecoveryTargets(normalizedTargets, itemsByClientKey)
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
      const lookupMutationVersion = recoveryTaskMutationVersionRef.current;
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
        mergeRecoveryLookupTasks(response.items ?? [], lookupMutationVersion);
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
    mergeRecoveryLookupTasks,
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
