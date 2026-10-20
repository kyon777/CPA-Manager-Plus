import type {
  MonitoringAccountHistoryTarget,
  MonitoringAccountLatestRequest,
  MonitoringAccountRecentIndexRequest,
} from '@/services/api/usageService';

export const ACCOUNT_RECENT_REQUEST_WINDOW_SIZE = 9;
export const ACCOUNT_RECENT_REQUEST_MATCH_THRESHOLD = 2;
export const ACCOUNT_RECENT_REQUEST_OPERATIONAL_FILTERS = [
  'recent_server_overloaded_502',
  'recent_failures',
] as const;

export type AccountRecentRequestOperationalFilter =
  (typeof ACCOUNT_RECENT_REQUEST_OPERATIONAL_FILTERS)[number];

const SERVER_OVERLOADED_CODE = 'server_is_overloaded';

export const isAccountRecentRequestOperationalFilter = (
  value: string
): value is AccountRecentRequestOperationalFilter =>
  ACCOUNT_RECENT_REQUEST_OPERATIONAL_FILTERS.includes(
    value as AccountRecentRequestOperationalFilter
  );

const hasExactSafeErrorCode = (value: string | null | undefined): boolean =>
  String(value ?? '')
    .toLowerCase()
    .split(/[^a-z0-9_]+/)
    .includes(SERVER_OVERLOADED_CODE);

const isServerOverloaded502 = (value: MonitoringAccountLatestRequest): boolean =>
  value.failed === true &&
  value.fail_status_code === 502 &&
  (hasExactSafeErrorCode(value.header_error_code) || hasExactSafeErrorCode(value.fail_summary));

export const getAccountRecentIndexLimitForOperationalFilter = (value: string): number =>
  isAccountRecentRequestOperationalFilter(value) ? ACCOUNT_RECENT_REQUEST_WINDOW_SIZE : 1;

export const buildAccountRecentIndexRequest = (
  accounts: MonitoringAccountHistoryTarget[],
  operationalFilter: string
): MonitoringAccountRecentIndexRequest => {
  const recentLimit = getAccountRecentIndexLimitForOperationalFilter(operationalFilter);
  return recentLimit === 1 ? { accounts } : { accounts, recent_limit: recentLimit };
};

export const matchesAccountRecentRequestAnomaly = (
  filter: AccountRecentRequestOperationalFilter,
  requests: readonly MonitoringAccountLatestRequest[] | null | undefined
): boolean => {
  const recent = (requests ?? []).slice(0, ACCOUNT_RECENT_REQUEST_WINDOW_SIZE);
  const matches = recent.filter((item) =>
    filter === 'recent_server_overloaded_502' ? isServerOverloaded502(item) : item.failed === true
  ).length;
  return matches >= ACCOUNT_RECENT_REQUEST_MATCH_THRESHOLD;
};
