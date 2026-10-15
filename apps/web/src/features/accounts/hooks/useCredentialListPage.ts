import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  credentialListApi,
  type CredentialListRequest,
  type CredentialListResponse,
} from '@/services/api';
import type { ApiClientRequestScope } from '@/services/api/client';

export type UseCredentialListPageOptions = {
  active: boolean;
  managerRequestScope?: ApiClientRequestScope;
  request: CredentialListRequest;
  revision?: number;
};

export type UseCredentialListPageResult = {
  data: CredentialListResponse | null;
  loading: boolean;
  error: string;
  status?: number;
  reload: () => Promise<void>;
};

const readError = (value: unknown): string => {
  if (value instanceof Error && value.message.trim()) return value.message.trim();
  if (typeof value === 'string' && value.trim()) return value.trim();
  return 'Credential list unavailable';
};

const readStatus = (value: unknown): number | undefined => {
  if (!value || typeof value !== 'object') return undefined;
  const status = (value as { status?: unknown }).status;
  return typeof status === 'number' && Number.isFinite(status) ? status : undefined;
};

export const useCredentialListPage = ({
  active,
  managerRequestScope,
  request,
  revision = 0,
}: UseCredentialListPageOptions): UseCredentialListPageResult => {
  const apiBase = managerRequestScope?.apiBase.trim() ?? '';
  const managementKey = managerRequestScope?.managementKey ?? '';
  const enabled = active && Boolean(apiBase && managementKey);
  // Callers commonly construct the request inline. Depend on its scalar
  // fields rather than object identity, otherwise every state update would
  // retrigger the effect and create an endless request loop.
  const normalizedRequest = useMemo<CredentialListRequest>(
    () => ({
      include_proxy_url: request.include_proxy_url,
      page: request.page,
      page_size: request.page_size,
      provider: request.provider,
      search: request.search,
      sort: request.sort,
      status: request.status,
    }),
    [
      request.include_proxy_url,
      request.page,
      request.page_size,
      request.provider,
      request.search,
      request.sort,
      request.status,
    ]
  );
  const requestKey = useMemo(() => JSON.stringify(normalizedRequest), [normalizedRequest]);
  const [data, setData] = useState<CredentialListResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [status, setStatus] = useState<number | undefined>(undefined);
  const generationRef = useRef(0);
  const abortRef = useRef<AbortController | null>(null);

  const reload = useCallback(async () => {
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    abortRef.current?.abort();
    abortRef.current = null;
    if (!enabled) {
      setData(null);
      setLoading(false);
      setError('');
      setStatus(undefined);
      return;
    }

    const controller = new AbortController();
    abortRef.current = controller;
    setLoading(true);
    setError('');
    setStatus(undefined);
    try {
      const response = await credentialListApi.list(
        apiBase,
        managementKey,
        normalizedRequest,
        controller.signal
      );
      if (generationRef.current !== generation || controller.signal.aborted) return;
      setData(response);
    } catch (requestError) {
      if (generationRef.current !== generation || controller.signal.aborted) return;
      setData(null);
      setError(readError(requestError));
      setStatus(readStatus(requestError));
    } finally {
      if (
        generationRef.current === generation &&
        abortRef.current === controller &&
        !controller.signal.aborted
      ) {
        setLoading(false);
      }
    }
  }, [apiBase, enabled, managementKey, normalizedRequest]);

  useEffect(() => {
    if (enabled) return;
    generationRef.current += 1;
    abortRef.current?.abort();
    abortRef.current = null;
    setData(null);
    setLoading(false);
    setError('');
    setStatus(undefined);
  }, [enabled]);

  useEffect(() => {
    if (!enabled) return;
    generationRef.current += 1;
    void reload();
    return () => {
      generationRef.current += 1;
      abortRef.current?.abort();
      abortRef.current = null;
    };
  }, [enabled, reload, requestKey, revision]);

  useEffect(() => {
    if (!enabled || data?.sync_status !== 'warming') return;
    const timer = setTimeout(() => {
      void reload();
    }, 1000);
    return () => clearTimeout(timer);
  }, [data?.sync_status, enabled, reload]);

  return { data, loading, error, status, reload };
};
