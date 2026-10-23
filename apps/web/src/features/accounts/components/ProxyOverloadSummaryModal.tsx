import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { IconCopy, IconRefreshCw } from '@/components/ui/icons';
import {
  filterProxyOverloadRows,
  proxyURLsForCopy,
  selectAllVisibleProxyKeys,
  sortProxyOverloadRows,
  successRateForProxy,
  toggleProxyOverloadSelection,
  unobservedProxyRows,
  type ProxyOverloadFilter,
  type ProxyOverloadSuccessSort,
  type ProxyURLRow,
} from '@/features/accounts/model/proxyOverloadSummary';
import type { ProxyOverloadSummaryResponse } from '@/services/api/usageService';
import styles from './ProxyOverloadSummaryModal.module.scss';

type ProxyOverloadSummaryModalProps = {
  open: boolean;
  loading: boolean;
  refreshing: boolean;
  result: ProxyOverloadSummaryResponse | null;
  error: string;
  onClose: () => void;
  onRefresh: () => void;
  onCopy: (text: string) => Promise<void>;
};

const formatRate = (value: number): string => {
  const normalized = Math.max(0, Math.min(1, Number.isFinite(value) ? value : 0));
  return `${(normalized * 100).toFixed(1).replace(/\.0$/, '')}%`;
};

export function ProxyOverloadSummaryModal({
  open,
  loading,
  refreshing,
  result,
  error,
  onClose,
  onRefresh,
  onCopy,
}: ProxyOverloadSummaryModalProps) {
  const { t } = useTranslation();
  const [filter, setFilter] = useState<ProxyOverloadFilter>('all');
  const [successSort, setSuccessSort] = useState<ProxyOverloadSuccessSort>(null);
  const [selected, setSelected] = useState<Set<string>>(() => new Set());
  const [copying, setCopying] = useState(false);
  const savedListAvailable = result?.saved_proxy_list_available === true;

  useEffect(() => {
    setFilter('all');
    setSuccessSort(null);
    setSelected(new Set());
  }, [result?.generated_at_ms]);

  useEffect(() => {
    if (!savedListAvailable && filter === 'unobserved') {
      setFilter('all');
    }
  }, [filter, savedListAvailable]);

  const sortedObservedRows = useMemo(
    () => sortProxyOverloadRows(result?.proxies ?? [], successSort),
    [result?.proxies, successSort]
  );
  const observedRows = useMemo(
    () => (filter === 'unobserved' ? [] : filterProxyOverloadRows(sortedObservedRows, filter)),
    [filter, sortedObservedRows]
  );
  const unobservedRows = useMemo(
    () => unobservedProxyRows(result?.unobserved_proxies ?? []),
    [result?.unobserved_proxies]
  );
  const visibleRows: readonly ProxyURLRow[] =
    filter === 'unobserved' ? unobservedRows : observedRows;
  const selectedVisibleCount = visibleRows.filter((row) => selected.has(row.proxy_url)).length;
  const allVisibleSelected = visibleRows.length > 0 && selectedVisibleCount === visibleRows.length;
  const selectedProxyURLs = proxyURLsForCopy(visibleRows, selected);
  const busy = loading || refreshing || copying;

  const toggleVisible = () => {
    setSelected((current) =>
      allVisibleSelected
        ? new Set([...current].filter((key) => !visibleRows.some((row) => row.proxy_url === key)))
        : new Set([...current, ...selectAllVisibleProxyKeys(visibleRows)])
    );
  };

  const handleCopy = async () => {
    const text = selectedProxyURLs;
    if (!text || copying) return;
    setCopying(true);
    try {
      await onCopy(text);
    } finally {
      setCopying(false);
    }
  };

  const setActiveFilter = (next: ProxyOverloadFilter) => {
    setFilter(next);
  };

  const toggleSuccessSort = () => {
    setSuccessSort((current) => (current === 'success-desc' ? 'success-asc' : 'success-desc'));
  };

  const filters: ProxyOverloadFilter[] = savedListAvailable
    ? ['all', 'zero', 'overloaded', 'unobserved']
    : ['all', 'zero', 'overloaded'];
  const successAriaSort =
    successSort === 'success-asc'
      ? 'ascending'
      : successSort === 'success-desc'
        ? 'descending'
        : 'none';

  return (
    <Modal
      open={open}
      onClose={onClose}
      closeDisabled={copying}
      title={t('accounts.proxy_overload_title')}
      width={880}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={copying}>
            {t('common.cancel')}
          </Button>
          <Button variant="secondary" onClick={onRefresh} disabled={busy} loading={refreshing}>
            {!refreshing ? <IconRefreshCw size={15} /> : null}
            {t('accounts.proxy_overload_refresh')}
          </Button>
          <Button
            onClick={() => void handleCopy()}
            disabled={busy || !selectedProxyURLs}
            loading={copying}
          >
            {!copying ? <IconCopy size={15} /> : null}
            {t('accounts.proxy_overload_copy_selected')}
          </Button>
        </>
      }
    >
      <div className={styles.body}>
        <p className={styles.hint}>{t('accounts.proxy_overload_hint')}</p>

        {result ? (
          <>
            <div className={styles.summary} aria-live="polite">
              <span>
                {t('accounts.proxy_overload_active', { count: result.active_account_count })}
              </span>
              <span>
                {t('accounts.proxy_overload_missing', {
                  count: result.missing_proxy_account_count,
                })}
              </span>
              <span>{t('accounts.proxy_overload_proxy_count', { count: result.proxy_count })}</span>
              <span>
                {t('accounts.proxy_overload_accounts', { count: result.overloaded_account_count })}
              </span>
              {savedListAvailable ? (
                <>
                  <span>
                    {t('accounts.proxy_overload_saved_count', {
                      count: result.saved_proxy_count ?? 0,
                    })}
                  </span>
                  <span>
                    {t('accounts.proxy_overload_observed_saved_count', {
                      count: result.observed_saved_proxy_count ?? 0,
                    })}
                  </span>
                  <span>
                    {t('accounts.proxy_overload_unobserved_count', {
                      count: result.unobserved_proxy_count ?? unobservedRows.length,
                    })}
                  </span>
                </>
              ) : null}
            </div>
            {!savedListAvailable ? (
              <p className={styles.savedUnavailable}>
                {t('accounts.proxy_overload_saved_unavailable')}
              </p>
            ) : null}
            <p className={styles.generated}>
              {t('accounts.proxy_overload_generated', {
                time: new Date(result.generated_at_ms).toLocaleString(),
              })}
            </p>
          </>
        ) : null}

        {error ? <div className={styles.error}>{error}</div> : null}
        {(loading || refreshing) && !result ? (
          <div className={styles.loading}>{t('accounts.proxy_overload_loading')}</div>
        ) : null}

        {result ? (
          <>
            <div className={styles.filters}>
              {filters.map((next) => (
                <Button
                  key={next}
                  variant={filter === next ? 'primary' : 'secondary'}
                  size="sm"
                  onClick={() => setActiveFilter(next)}
                  disabled={busy}
                >
                  {t(`accounts.proxy_overload_filter_${next}`)}
                </Button>
              ))}
              <Button
                variant="secondary"
                size="sm"
                onClick={toggleVisible}
                disabled={busy || visibleRows.length === 0}
              >
                {t(
                  allVisibleSelected
                    ? 'accounts.proxy_overload_clear_visible'
                    : 'accounts.proxy_overload_select_visible'
                )}
              </Button>
            </div>

            {visibleRows.length === 0 ? (
              <div className={styles.empty}>
                {filter === 'unobserved'
                  ? t('accounts.proxy_overload_unobserved_empty')
                  : t('accounts.proxy_overload_empty')}
              </div>
            ) : (
              <div className={styles.tableWrap}>
                {filter === 'unobserved' ? (
                  <table className={styles.table}>
                    <thead>
                      <tr>
                        <th aria-label={t('accounts.proxy_overload_select_visible')} />
                        <th>{t('accounts.proxy_overload_url')}</th>
                        <th>{t('accounts.proxy_overload_status_label')}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {unobservedRows.map((row) => (
                        <tr
                          key={row.proxy_url}
                          data-proxy-overload-row
                          data-proxy-overload-kind="unobserved"
                          data-proxy-url={row.proxy_url}
                        >
                          <td>
                            <input
                              aria-label={row.proxy_url}
                              type="checkbox"
                              checked={selected.has(row.proxy_url)}
                              disabled={busy}
                              onChange={() =>
                                setSelected((current) =>
                                  toggleProxyOverloadSelection(current, row.proxy_url)
                                )
                              }
                            />
                          </td>
                          <td>
                            <code title={row.proxy_url}>{row.proxy_url}</code>
                          </td>
                          <td className={styles.unobservedStatus}>
                            {t('accounts.proxy_overload_unobserved_status')}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <table className={styles.table}>
                    <thead>
                      <tr>
                        <th aria-label={t('accounts.proxy_overload_select_visible')} />
                        <th>{t('accounts.proxy_overload_url')}</th>
                        <th>{t('accounts.proxy_overload_active_label')}</th>
                        <th>{t('accounts.proxy_overload_overloaded_label')}</th>
                        <th>{t('accounts.proxy_overload_non_overloaded_label')}</th>
                        <th aria-sort={successAriaSort}>
                          <button
                            type="button"
                            className={styles.sortButton}
                            data-proxy-overload-success-sort
                            onClick={toggleSuccessSort}
                            disabled={busy || sortedObservedRows.length === 0}
                          >
                            {t('accounts.proxy_overload_success_rate_label')}
                            {successSort === 'success-desc'
                              ? ' ↓'
                              : successSort === 'success-asc'
                                ? ' ↑'
                                : ' ↕'}
                          </button>
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {observedRows.map((row) => (
                        <tr
                          key={row.proxy_url}
                          data-proxy-overload-row
                          data-proxy-overload-kind="observed"
                          data-proxy-url={row.proxy_url}
                        >
                          <td>
                            <input
                              aria-label={row.proxy_url}
                              type="checkbox"
                              checked={selected.has(row.proxy_url)}
                              disabled={busy}
                              onChange={() =>
                                setSelected((current) =>
                                  toggleProxyOverloadSelection(current, row.proxy_url)
                                )
                              }
                            />
                          </td>
                          <td>
                            <code title={row.proxy_url}>{row.proxy_url}</code>
                          </td>
                          <td>{row.active_account_count}</td>
                          <td>{row.overloaded_account_count}</td>
                          <td>{row.non_overloaded_account_count}</td>
                          <td>{formatRate(successRateForProxy(row))}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </div>
            )}
          </>
        ) : null}
      </div>
    </Modal>
  );
}
