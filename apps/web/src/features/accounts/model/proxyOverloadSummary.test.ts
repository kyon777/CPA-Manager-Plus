import { describe, expect, it } from 'vitest';
import type { ProxyOverloadSummaryRow } from '@/services/api/usageService';
import {
  filterProxyOverloadRows,
  proxyURLsForCopy,
  selectAllVisibleProxyKeys,
  sortProxyOverloadRows,
  toggleProxyOverloadSelection,
} from './proxyOverloadSummary';

const rows: ProxyOverloadSummaryRow[] = [
  {
    proxy_url: 'http://loaded:8080',
    active_account_count: 90,
    overloaded_account_count: 30,
    non_overloaded_account_count: 60,
    overloaded_rate: 1 / 3,
  },
  {
    proxy_url: 'http://healthy-a:8080',
    active_account_count: 20,
    overloaded_account_count: 0,
    non_overloaded_account_count: 20,
    overloaded_rate: 0,
  },
  {
    proxy_url: 'http://healthy-b:8080',
    active_account_count: 40,
    overloaded_account_count: 0,
    non_overloaded_account_count: 40,
    overloaded_rate: 0,
  },
];

describe('proxyOverloadSummary model', () => {
  it('sorts zero-overload rows before overloaded rows, then active count then normalized proxy', () => {
    expect(sortProxyOverloadRows(rows).map((row) => row.proxy_url)).toEqual([
      'http://healthy-b:8080',
      'http://healthy-a:8080',
      'http://loaded:8080',
    ]);
  });

  it('filters all, zero, and overloaded rows without mutating result data', () => {
    expect(filterProxyOverloadRows(rows, 'all')).toHaveLength(3);
    expect(filterProxyOverloadRows(rows, 'zero')).toEqual([rows[1], rows[2]]);
    expect(filterProxyOverloadRows(rows, 'overloaded')).toEqual([rows[0]]);
    expect(rows).toHaveLength(3);
  });

  it('selects visible proxies and copies only selected URLs with LF separators and no trailing newline', () => {
    const visible = sortProxyOverloadRows(rows);
    const selected = selectAllVisibleProxyKeys(visible);
    expect(selected).toEqual(
      new Set(['http://healthy-b:8080', 'http://healthy-a:8080', 'http://loaded:8080'])
    );

    const toggled = toggleProxyOverloadSelection(selected, 'http://healthy-a:8080');
    expect(toggled.has('http://healthy-a:8080')).toBe(false);
    expect(proxyURLsForCopy(visible, toggled)).toBe(
      'http://healthy-b:8080\nhttp://loaded:8080'
    );
  });

  it('treats terminal slashes as equal only for stable comparison order', () => {
    const equalRows: ProxyOverloadSummaryRow[] = [
      { ...rows[0], proxy_url: 'http://same:8080////', active_account_count: 1 },
      { ...rows[1], proxy_url: 'http://same:8080/', active_account_count: 1 },
    ];
    expect(sortProxyOverloadRows(equalRows).map((row) => row.proxy_url)).toEqual([
      'http://same:8080/',
      'http://same:8080////',
    ]);
  });
});
