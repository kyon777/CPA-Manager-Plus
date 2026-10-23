import type { ProxyOverloadSummaryRow } from '@/services/api/usageService';

export type ProxyOverloadFilter = 'all' | 'zero' | 'overloaded' | 'unobserved';
export type ProxyOverloadSuccessSort = 'success-desc' | 'success-asc' | null;
export type ProxyURLRow = Pick<ProxyOverloadSummaryRow, 'proxy_url'>;

const normalizeProxySortKey = (value: string): string => value.trim().replace(/\/+$/, '');

const defaultProxyOverloadComparison = (
  left: ProxyOverloadSummaryRow,
  right: ProxyOverloadSummaryRow
): number => {
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
};

export const successRateForProxy = (row: ProxyOverloadSummaryRow): number =>
  row.active_account_count > 0
    ? row.non_overloaded_account_count / row.active_account_count
    : 0;

export const sortProxyOverloadRows = (
  rows: readonly ProxyOverloadSummaryRow[],
  successSort: ProxyOverloadSuccessSort = null
): ProxyOverloadSummaryRow[] =>
  [...rows].sort((left, right) => {
    if (successSort) {
      const successDifference = successRateForProxy(left) - successRateForProxy(right);
      if (successDifference !== 0) {
        return successSort === 'success-desc' ? -successDifference : successDifference;
      }
    }
    return defaultProxyOverloadComparison(left, right);
  });

export const filterProxyOverloadRows = (
  rows: readonly ProxyOverloadSummaryRow[],
  filter: Exclude<ProxyOverloadFilter, 'unobserved'>
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

export const unobservedProxyRows = (values: readonly string[]): ProxyURLRow[] => {
  const seen = new Set<string>();
  const result: ProxyURLRow[] = [];
  for (const value of values) {
    const key = normalizeProxySortKey(value);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    result.push({ proxy_url: key });
  }
  return result;
};

export const visibleProxyOverloadRows = (
  observedRows: readonly ProxyOverloadSummaryRow[],
  unobservedRows: readonly ProxyURLRow[],
  filter: ProxyOverloadFilter
): ProxyURLRow[] =>
  filter === 'unobserved'
    ? [...unobservedRows]
    : filterProxyOverloadRows(observedRows, filter);

export const selectAllVisibleProxyKeys = (rows: readonly ProxyURLRow[]): Set<string> =>
  new Set(rows.map((row) => row.proxy_url));

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
  rows: readonly ProxyURLRow[],
  selected: ReadonlySet<string>
): string =>
  rows
    .filter((row) => selected.has(row.proxy_url))
    .map((row) => row.proxy_url)
    .join('\n');
