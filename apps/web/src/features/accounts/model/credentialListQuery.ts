import type { CredentialListRequest } from '@/services/api';
import type { AccountQuotaBand, AccountRowSort } from './accountRows';
import type { AccountOperationalFilter } from './accountsWorkspaceUiState';

export interface CredentialListQueryInput {
  page: number;
  pageSize: number;
  search?: string;
  provider?: string;
  status?: string;
  sort?: AccountRowSort;
}

export interface ServerCredentialListEligibilityInput {
  activeView: 'accounts' | 'health' | 'oauth' | string;
  managerStorageAvailable: boolean;
  credentialListAvailable?: boolean;
  pageSize: number;
  provider: string;
  status: string;
  plan: string;
  quotaBand: AccountQuotaBand;
  operational: AccountOperationalFilter;
  sort: AccountRowSort;
}

const SERVER_STATUS_FILTERS = new Set(['all', 'enabled', 'disabled']);
const SERVER_SORT_KEYS = new Set(['default', 'name', 'priority', 'recent']);

const normalizeSort = (sort: AccountRowSort | undefined): string => {
  const key = sort?.key ?? 'default';
  if (key === 'default') return 'recent_desc';
  return `${key}_${sort?.direction === 'asc' ? 'asc' : 'desc'}`;
};

export const buildCredentialListRequest = (
  input: CredentialListQueryInput
): CredentialListRequest => {
  const request: CredentialListRequest = {
    page: Math.max(1, Math.trunc(input.page || 1)),
    page_size: Math.min(50, Math.max(1, Math.trunc(input.pageSize || 50))),
    include_proxy_url: 1,
    sort: normalizeSort(input.sort),
  };
  const search = input.search?.trim();
  const provider = input.provider?.trim();
  const status = input.status?.trim();
  if (search) request.search = search;
  if (provider && provider !== 'all') request.provider = provider;
  if (status && status !== 'all') request.status = status;
  return request;
};

export const isServerCredentialListEligible = (
  input: ServerCredentialListEligibilityInput
): boolean =>
  input.activeView === 'accounts' &&
  input.managerStorageAvailable &&
  input.credentialListAvailable === true &&
  input.pageSize >= 1 &&
  input.pageSize <= 50 &&
  SERVER_STATUS_FILTERS.has(input.status.trim().toLowerCase() || 'all') &&
  input.plan.trim().toLowerCase() === 'all' &&
  input.quotaBand === 'all' &&
  input.operational === 'all' &&
  SERVER_SORT_KEYS.has(input.sort.key);
