import { sha256RawTextHex } from '@/utils/apiKeyHash';

export type ProxyReplacementSource = 'inspection' | 'batch_uniform' | 'batch_pool';
export type ProxyReplacementMode = 'uniform' | 'pool';
export type ProxyReplacementTaskStatus =
  | 'draft'
  | 'queued'
  | 'running'
  | 'completed'
  | 'partial'
  | 'failed'
  | 'expired'
  | 'unavailable';
export type ProxyReplacementItemStatus =
  | 'planned'
  | 'writing'
  | 'updated'
  | 'skipped_stale'
  | 'write_failed'
  | 'recheck_running'
  | 'recheck_succeeded'
  | 'recheck_failed';
export type ProxyPoolStatus = 'normal' | 'suspect';

export interface ProxyReplacementTarget {
  clientKey: string;
  fileName: string;
  authIndex?: string;
  accountEmail?: string;
  provider: string;
  runtimeId?: string;
  accountId?: string;
  accountSnapshot?: string;
}

export interface ProxyReplacementTask {
  id: number;
  source: ProxyReplacementSource;
  mode: ProxyReplacementMode;
  status: ProxyReplacementTaskStatus;
  recheckAfterWrite: boolean;
  canConfirm: boolean;
  taskOffset: number;
  totalCount: number;
  updatedCount: number;
  failedCount: number;
  skippedCount: number;
  recheckSuccessCount: number;
  recheckFailedCount: number;
  unusedNormalCount: number;
  fallbackNormalCount: number;
  suspectExcludedCount: number;
  expiresAtMs?: number;
  createdAtMs: number;
  updatedAtMs: number;
}

export interface ProxyReplacementTaskItem {
  id: number;
  taskId: number;
  itemOrder: number;
  target: ProxyReplacementTarget;
  oldProxyUrl?: string;
  oldProxyFingerprint?: string;
  newProxyUrl?: string;
  newProxyFingerprint?: string;
  allocationSource?: string;
  status: ProxyReplacementItemStatus;
  writeErrorCode?: string;
  writeErrorMessage?: string;
  recheckErrorCode?: string;
  recheckErrorMessage?: string;
}

export interface ProxyReplacementTaskPage {
  task: ProxyReplacementTask;
  items: ProxyReplacementTaskItem[];
  page: number;
  pageSize: number;
  total: number;
}

export interface LocalProxyFailureEvidence {
  proxyUrl?: string;
  statusCode?: number;
  errorKind?: string;
  errorDetail?: string;
}

/** Matches the server's proxy-pool identity semantics exactly. */
export const normalizeProxyReplacementURL = (value: unknown): string => {
  if (typeof value !== 'string') return '';
  const trimmed = value.trim();
  return trimmed ? trimmed.replace(/\/+$/, '') : '';
};

/** Matches the SHA-256 identity written by the Manager Server. */
export const proxyReplacementFingerprint = (value: unknown): string => {
  const normalized = normalizeProxyReplacementURL(value);
  return normalized ? sha256RawTextHex(normalized) : '';
};

/** Keep this deliberately aligned with model.IsSuspectedProxyTransportFailure. */
export const isLocalProxySuspect = (evidence: LocalProxyFailureEvidence): boolean => {
  if (!normalizeProxyReplacementURL(evidence.proxyUrl)) return false;
  const detail = String(evidence.errorDetail ?? '').trim().toLowerCase();
  const kind = String(evidence.errorKind ?? '').trim().toLowerCase();
  if (
    evidence.statusCode === 401 ||
    evidence.statusCode === 429 ||
    detail.includes('quota') ||
    detail.includes('credit') ||
    kind !== 'request_error'
  ) {
    return false;
  }
  if (detail.includes('context deadline exceeded') || detail.includes('timeout')) return true;
  const compact = detail.replace(/[\s]/g, '');
  return compact.includes('api-callfailed:502') && compact.includes('"error":"requestfailed"');
};

export const isProxyReplacementTaskTerminal = (status: ProxyReplacementTaskStatus): boolean =>
  status === 'completed' ||
  status === 'partial' ||
  status === 'failed' ||
  status === 'expired' ||
  status === 'unavailable';
