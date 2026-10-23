import type { ProxyOverloadSummaryRow } from '@/services/api/usageService';

export type ProxyOverloadFilter = 'all' | 'zero' | 'overloaded';

const normalizeProxySortKey = (value: string): string => value.trim().replace(/\/+$/, '');

export const sortProxyOverloadRows = (
  rows: readonly ProxyOverloadSummaryRow[]
): ProxyOverloadSummaryRow[] =>
  [...rows].sort((left, right) => {
    const leftHasOverload = left.overloaded_account_count > 0;
    const rightHasOverload = right.overloaded_account_count > 0;
    if (leftHasOverload !== rightHasOverload) return leftHasOverload ? 1 : -1;
    if (left.active_account_count !== right.active_account_count) {
      return right.active_account_count - left.active_account_count;
    }
    const keyComparison = normalizeProxySortKey(left.proxy_url).localeCompare(
      normalizeProxySortKey(right.proxy_url)
    );
    if (keyComparison !== 0) return keyComparison;
    return left.proxy_url.localeCompare(right.proxy_url);
  });

export const filterProxyOverloadRows = (
  rows: readonly ProxyOverloadSummaryRow[],
  filter: ProxyOverloadFilter
): ProxyOverloadSummaryRow[] => {
  switch (filter) {
    case 'zero':
      return rows.filter((row) => row.overloaded_account_count === 0);
    case 'overloaded':
      return rows.filter((row) => row.overloaded_account_count > 0);
    default:
      return [...rows];
  }
};

export const selectAllVisibleProxyKeys = (
  rows: readonly ProxyOverloadSummaryRow[]
): Set<string> => new Set(rows.map((row) => row.proxy_url));

export const toggleProxyOverloadSelection = (
  selected: ReadonlySet<string>,
  proxyURL: string
): Set<string> => {
  const next = new Set(selected);
  if (next.has(proxyURL)) {
    next.delete(proxyURL);
  } else {
    next.add(proxyURL);
  }
  return next;
};

export const proxyURLsForCopy = (
  rows: readonly ProxyOverloadSummaryRow[],
  selected: ReadonlySet<string>
): string =>
  rows
    .filter((row) => selected.has(row.proxy_url))
    .map((row) => row.proxy_url)
    .join('\n');
