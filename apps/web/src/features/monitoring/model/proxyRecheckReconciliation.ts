import type { ProxyReplacementTaskItem } from '@/services/api/usageService';
import type { CodexInspectionResultItem } from '@/features/monitoring/codexInspection';

// A local inspection has no durable server result row. Direct exit-IP success
// proves the new proxy works; an inconclusive echo leaves no proof of failure.
// Both must clear the old direct-failure marker, while a direct failure remains
// visible for manual follow-up.
export const reconcileProxyReplacementRechecks = (
  rows: readonly CodexInspectionResultItem[],
  taskItems: readonly ProxyReplacementTaskItem[]
): CodexInspectionResultItem[] => {
  const resolvedStatuses = new Map(
    taskItems
      .filter(
        (item) =>
          item.status === 'recheck_succeeded' || item.status === 'recheck_inconclusive'
      )
      .map((item) => [item.target.clientKey.trim(), item.status] as const)
      .filter(([clientKey]) => Boolean(clientKey))
  );
  if (resolvedStatuses.size === 0) return [...rows];

  return rows.map((row) => {
    const status = resolvedStatuses.get(row.key.trim());
    if (!status) return row;
    const { proxyVerificationSource: _source, ...withoutVerification } = row;
    return {
      ...withoutVerification,
      proxySuspect: false,
      ...(status === 'recheck_succeeded' ? { proxyRecheckSucceeded: true } : {}),
    };
  });
};
