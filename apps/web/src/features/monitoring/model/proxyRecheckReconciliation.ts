import type { ProxyReplacementTaskItem } from '@/services/api/usageService';
import type { CodexInspectionResultItem } from '@/features/monitoring/codexInspection';

// A local inspection has no durable server result row.  Preserve its original
// evidence, but record that a directed post-write probe already verified the
// replacement proxy so the old timeout/transport detail cannot re-classify it
// as abnormal during subsequent renders or after a browser refresh.
export const reconcileSuccessfulProxyRechecks = (
  rows: readonly CodexInspectionResultItem[],
  taskItems: readonly ProxyReplacementTaskItem[]
): CodexInspectionResultItem[] => {
  const succeededKeys = new Set(
    taskItems
      .filter((item) => item.status === 'recheck_succeeded')
      .map((item) => item.target.clientKey.trim())
      .filter(Boolean)
  );
  if (succeededKeys.size === 0) return [...rows];

  return rows.map((row) => {
    if (!succeededKeys.has(row.key.trim()) || row.proxyRecheckSucceeded === true) return row;
    return {
      ...row,
      proxySuspect: false,
      proxyRecheckSucceeded: true,
    };
  });
};
