import { describe, expect, it } from 'vitest';
import type {
  MonitoringAccountHistoryTarget,
  MonitoringAccountLatestRequest,
} from '@/services/api/usageService';
import {
  buildAccountRecentIndexRequest,
  getAccountRecentIndexLimitForOperationalFilter,
  matchesAccountRecentRequestAnomaly,
} from './accountRecentRequestAnomaly';

const request = (
  timestamp_ms: number,
  overrides: Partial<MonitoringAccountLatestRequest> = {}
): MonitoringAccountLatestRequest => ({ timestamp_ms, failed: false, ...overrides });

const overloaded = (timestamp_ms: number): MonitoringAccountLatestRequest =>
  request(timestamp_ms, {
    failed: true,
    fail_status_code: 502,
    header_error_code: 'server_is_overloaded',
  });

const target: MonitoringAccountHistoryTarget = { row_key: 'row-1' };

describe('accountRecentRequestAnomaly', () => {
  it('matches two exact overloaded 502 records in the latest nine requests', () => {
    expect(
      matchesAccountRecentRequestAnomaly('recent_server_overloaded_502', [
        overloaded(9),
        overloaded(8),
        request(7),
        request(6),
        request(5),
        request(4),
        request(3),
        request(2),
        request(1),
      ])
    ).toBe(true);
  });

  it('does not match generic 502, EOF, proxy 407, or an overload code on another status', () => {
    expect(
      matchesAccountRecentRequestAnomaly('recent_server_overloaded_502', [
        request(9, { failed: true, fail_status_code: 502, fail_summary: 'unexpected EOF' }),
        request(8, { failed: true, fail_status_code: 502, header_error_code: 'proxy_connect_407' }),
        request(7, { failed: true, fail_status_code: 500, header_error_code: 'server_is_overloaded' }),
        overloaded(6),
      ])
    ).toBe(false);
  });

  it('accepts the exact overload code from a safe JSON summary but not a larger token', () => {
    expect(
      matchesAccountRecentRequestAnomaly('recent_server_overloaded_502', [
        request(9, {
          failed: true,
          fail_status_code: 502,
          fail_summary: '{"code":"server_is_overloaded"}',
        }),
        overloaded(8),
      ])
    ).toBe(true);
    expect(
      matchesAccountRecentRequestAnomaly('recent_server_overloaded_502', [
        request(9, {
          failed: true,
          fail_status_code: 502,
          fail_summary: '{"code":"not_server_is_overloaded"}',
        }),
        overloaded(8),
      ])
    ).toBe(false);
  });

  it('matches any two failures, including fewer than nine stored requests', () => {
    expect(
      matchesAccountRecentRequestAnomaly('recent_failures', [
        request(9, { failed: true }),
        request(8, { failed: true }),
        request(7),
      ])
    ).toBe(true);
  });

  it('ignores matching failures outside the latest nine requests', () => {
    expect(
      matchesAccountRecentRequestAnomaly('recent_failures', [
        ...Array.from({ length: 9 }, (_, offset) => request(9 - offset)),
        request(0, { failed: true }),
        request(-1, { failed: true }),
      ])
    ).toBe(false);
  });

  it('keeps normal operational filters on the compact one-record request', () => {
    expect(getAccountRecentIndexLimitForOperationalFilter('all')).toBe(1);
    expect(buildAccountRecentIndexRequest([target], 'all')).toEqual({ accounts: [target] });
  });

  it('requests nine records only for either anomaly filter', () => {
    expect(getAccountRecentIndexLimitForOperationalFilter('recent_server_overloaded_502')).toBe(9);
    expect(getAccountRecentIndexLimitForOperationalFilter('recent_failures')).toBe(9);
    expect(buildAccountRecentIndexRequest([target], 'recent_failures')).toEqual({
      accounts: [target],
      recent_limit: 9,
    });
  });
});
