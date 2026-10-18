import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { IconCopy, IconRefreshCw } from '@/components/ui/icons';
import {
  usageServiceApi,
  type ProxyReplacementTaskItem,
  type ProxyReplacementTaskPage,
} from '@/services/api/usageService';
import {
  isProxyReplacementTaskTerminal,
  type ProxyReplacementMode,
  type ProxyReplacementTarget,
} from '@/features/accounts/model/proxyReplacement';
import { copyToClipboard } from '@/utils/clipboard';
import styles from './ProxyReplacementModal.module.scss';

export type ProxyReplacementModalOrigin = 'inspection' | 'accounts';

export type ProxyReplacementCompletion = {
  task: ProxyReplacementTaskPage['task'];
  // Completion reconciliation must include every task item, not only the
  // modal's currently visible 50-row page.
  items: ProxyReplacementTaskItem[];
};

export type ProxyReplacementModalProps = {
  open: boolean;
  managerRequestScope?: {
    apiBase: string;
    managementKey?: string;
  };
  origin: ProxyReplacementModalOrigin;
  targets: readonly ProxyReplacementTarget[];
  onClose: () => void;
  onCompleted?: (completion: ProxyReplacementCompletion) => void;
};

const TASK_POLL_INTERVAL_MS = 700;
const TASK_PAGE_SIZE = 50;
const COMPLETION_PAGE_SIZE = 100;

const asErrorMessage = (error: unknown) =>
  error instanceof Error ? error.message : String(error || 'Proxy replacement request failed');

const taskStatusKey = (status: string) => `proxy_replacement.task_status_${status}`;

const itemStatusKey = (status: string) => `proxy_replacement.item_status_${status}`;

const isPollableTask = (page: ProxyReplacementTaskPage | null): page is ProxyReplacementTaskPage =>
  Boolean(page && (page.task.status === 'queued' || page.task.status === 'running'));

export function ProxyReplacementModal({
  open,
  managerRequestScope,
  origin,
  targets,
  onClose,
  onCompleted,
}: ProxyReplacementModalProps) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<ProxyReplacementMode>('pool');
  const [uniformProxyUrl, setUniformProxyUrl] = useState('');
  const [taskPage, setTaskPage] = useState<ProxyReplacementTaskPage | null>(null);
  const [busy, setBusy] = useState(false);
  const [loadingPage, setLoadingPage] = useState(false);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState(false);
  const [restoringSuspects, setRestoringSuspects] = useState(false);
  const completionNotifiedTaskRef = useRef<number | null>(null);
  const targetKey = useMemo(
    () =>
      targets
        .map((target) => `${target.clientKey}\u0000${target.fileName}\u0000${target.authIndex ?? ''}`)
        .join('\u0001'),
    [targets]
  );
  const inspectionMode = origin === 'inspection';
  const effectiveMode: ProxyReplacementMode = inspectionMode ? 'pool' : mode;

  useEffect(() => {
    if (!open) return;
    setMode('pool');
    setUniformProxyUrl('');
    setTaskPage(null);
    setBusy(false);
    setLoadingPage(false);
    setError('');
    setCopied(false);
    setRestoringSuspects(false);
    completionNotifiedTaskRef.current = null;
  }, [open, origin, targetKey]);

  const loadTaskPage = useCallback(
    async (taskId: number, page: number) => {
      if (!managerRequestScope) return;
      setLoadingPage(true);
      try {
        const next = await usageServiceApi.getProxyReplacementTask(
          managerRequestScope.apiBase,
          managerRequestScope.managementKey,
          taskId,
          page,
          TASK_PAGE_SIZE
        );
        setTaskPage(next);
        setError('');
      } catch (nextError) {
        setError(asErrorMessage(nextError));
      } finally {
        setLoadingPage(false);
      }
    },
    [managerRequestScope]
  );

  const loadCompletedTaskItems = useCallback(
    async (task: ProxyReplacementTaskPage['task']): Promise<ProxyReplacementTaskItem[]> => {
      if (!managerRequestScope) return [];
      const totalPages = Math.max(1, Math.ceil(task.totalCount / COMPLETION_PAGE_SIZE));
      const pages = await Promise.all(
        Array.from({ length: totalPages }, (_, index) =>
          usageServiceApi.getProxyReplacementTask(
            managerRequestScope.apiBase,
            managerRequestScope.managementKey,
            task.id,
            index + 1,
            COMPLETION_PAGE_SIZE
          )
        )
      );
      return pages.flatMap((page) => page.items);
    },
    [managerRequestScope]
  );

  useEffect(() => {
    if (!isPollableTask(taskPage) || !open || busy || !managerRequestScope) return;
    const timer = window.setTimeout(() => {
      void loadTaskPage(taskPage.task.id, taskPage.page);
    }, TASK_POLL_INTERVAL_MS);
    return () => window.clearTimeout(timer);
  }, [busy, loadTaskPage, managerRequestScope, open, taskPage]);

  useEffect(() => {
    if (!taskPage || !isProxyReplacementTaskTerminal(taskPage.task.status)) return;
    if (completionNotifiedTaskRef.current === taskPage.task.id) return;
    completionNotifiedTaskRef.current = taskPage.task.id;
    let cancelled = false;
    void loadCompletedTaskItems(taskPage.task)
      .then((items) => {
        if (!cancelled) onCompleted?.({ task: taskPage.task, items });
      })
      .catch((completionError) => {
        // The replacement itself is terminal. Do not relabel it as a write
        // failure if the optional UI reconciliation read fails; surface the
        // read issue and reconcile the currently visible page at minimum.
        if (cancelled) return;
        setError(asErrorMessage(completionError));
        onCompleted?.({ task: taskPage.task, items: taskPage.items });
      });
    return () => {
      cancelled = true;
    };
  }, [loadCompletedTaskItems, onCompleted, taskPage]);

  const handlePreview = useCallback(async () => {
    if (!managerRequestScope || targets.length === 0) return;
    if (effectiveMode === 'uniform' && !uniformProxyUrl.trim()) {
      setError(t('proxy_replacement.uniform_required'));
      return;
    }
    setBusy(true);
    setError('');
    setCopied(false);
    try {
      const page = await usageServiceApi.createProxyReplacementPreview(
        managerRequestScope.apiBase,
        managerRequestScope.managementKey,
        {
          source: inspectionMode
            ? 'inspection'
            : effectiveMode === 'uniform'
              ? 'batch_uniform'
              : 'batch_pool',
          mode: effectiveMode,
          ...(effectiveMode === 'uniform' ? { uniformProxyUrl: uniformProxyUrl.trim() } : {}),
          targets: [...targets],
        }
      );
      setTaskPage(page);
    } catch (previewError) {
      setError(asErrorMessage(previewError));
    } finally {
      setBusy(false);
    }
  }, [effectiveMode, inspectionMode, managerRequestScope, t, targets, uniformProxyUrl]);

  const handleConfirm = useCallback(async () => {
    if (!managerRequestScope || !taskPage?.task.canConfirm) return;
    setBusy(true);
    setError('');
    try {
      const task = await usageServiceApi.confirmProxyReplacementTask(
        managerRequestScope.apiBase,
        managerRequestScope.managementKey,
        taskPage.task.id
      );
      setTaskPage((current) =>
        current ? { ...current, task: { ...current.task, ...task } } : current
      );
      await loadTaskPage(task.id, taskPage.page);
    } catch (confirmError) {
      setError(asErrorMessage(confirmError));
    } finally {
      setBusy(false);
    }
  }, [loadTaskPage, managerRequestScope, taskPage]);

  const handleRestoreSuspectsAndRetry = useCallback(async () => {
    if (!managerRequestScope || taskPage?.task.status !== 'unavailable' || busy) return;
    setBusy(true);
    setRestoringSuspects(true);
    setError('');
    try {
      await usageServiceApi.restoreSuspectProxyPoolStates(
        managerRequestScope.apiBase,
        managerRequestScope.managementKey
      );
      // An unavailable preview contains a deliberately empty mapping. Rebuild
      // it only after the Manager-owned pool eligibility state is restored;
      // no credential, quota, billing, or request-history refresh occurs here.
      const page = await usageServiceApi.createProxyReplacementPreview(
        managerRequestScope.apiBase,
        managerRequestScope.managementKey,
        {
          source: inspectionMode
            ? 'inspection'
            : effectiveMode === 'uniform'
              ? 'batch_uniform'
              : 'batch_pool',
          mode: effectiveMode,
          ...(effectiveMode === 'uniform' ? { uniformProxyUrl: uniformProxyUrl.trim() } : {}),
          targets: [...targets],
        }
      );
      setTaskPage(page);
    } catch (restoreError) {
      setError(asErrorMessage(restoreError));
    } finally {
      setRestoringSuspects(false);
      setBusy(false);
    }
  }, [
    busy,
    effectiveMode,
    inspectionMode,
    managerRequestScope,
    targets,
    taskPage,
    uniformProxyUrl,
  ]);

  const mappingText = useMemo(
    () =>
      (taskPage?.items ?? [])
        .map((item) => {
          const label = item.target.accountEmail || item.target.accountSnapshot || item.target.fileName;
          return `${label} -> ${item.newProxyUrl || '-'}`;
        })
        .join('\n'),
    [taskPage?.items]
  );

  const handleCopyMapping = useCallback(async () => {
    if (!mappingText) return;
    const success = await copyToClipboard(mappingText);
    setCopied(success);
  }, [mappingText]);

  const task = taskPage?.task ?? null;
  const unavailable = task?.status === 'unavailable';
  const terminal = task ? isProxyReplacementTaskTerminal(task.status) : false;
  const hasPreviousPage = Boolean(taskPage && taskPage.page > 1);
  const hasNextPage = Boolean(taskPage && taskPage.page * taskPage.pageSize < taskPage.total);
  const closeDisabled = busy || loadingPage || Boolean(task && !terminal && task.status !== 'draft');

  return (
    <Modal
      open={open}
      onClose={onClose}
      closeDisabled={closeDisabled}
      title={t('proxy_replacement.title')}
      width={780}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={closeDisabled}>
            {t('common.cancel')}
          </Button>
          {taskPage ? (
            task?.canConfirm ? (
              <Button onClick={() => void handleConfirm()} loading={busy} disabled={busy || unavailable}>
                {t('proxy_replacement.confirm')}
              </Button>
            ) : null
          ) : (
            <Button
              onClick={() => void handlePreview()}
              loading={busy}
              disabled={busy || !managerRequestScope || targets.length === 0}
            >
              {t('proxy_replacement.preview')}
            </Button>
          )}
        </>
      }
    >
      <div className={styles.body}>
        <p className={styles.hint}>
          {inspectionMode
            ? t('proxy_replacement.inspection_hint')
            : t('proxy_replacement.accounts_hint', { count: targets.length })}
        </p>

        {!inspectionMode && !taskPage ? (
          <fieldset className={styles.modeFieldset} disabled={busy}>
            <legend>{t('proxy_replacement.mode_label')}</legend>
            <label className={styles.modeOption}>
              <input
                type="radio"
                checked={mode === 'pool'}
                onChange={() => setMode('pool')}
              />
              <span>{t('proxy_replacement.mode_pool')}</span>
            </label>
            <label className={styles.modeOption}>
              <input
                type="radio"
                checked={mode === 'uniform'}
                onChange={() => setMode('uniform')}
              />
              <span>{t('proxy_replacement.mode_uniform')}</span>
            </label>
          </fieldset>
        ) : null}

        {!inspectionMode && !taskPage && effectiveMode === 'uniform' ? (
          <Input
            label={t('proxy_replacement.uniform_label')}
            value={uniformProxyUrl}
            onChange={(event) => setUniformProxyUrl(event.target.value)}
            placeholder={t('proxy_replacement.uniform_placeholder')}
            disabled={busy}
            autoFocus
          />
        ) : null}

        {error ? <div className={styles.error} role="alert">{error}</div> : null}

        {taskPage ? (
          <section className={styles.preview} data-proxy-replacement-preview={true}>
            <div className={styles.summary}>
              <span className={styles.status} data-status={task?.status}>
                {t(taskStatusKey(task?.status ?? 'draft'))}
              </span>
              <strong>{t('proxy_replacement.target_count', { count: task?.totalCount ?? 0 })}</strong>
              {task?.mode === 'pool' ? (
                <span>
                  {t('proxy_replacement.pool_summary', {
                    unused: task.unusedNormalCount,
                    fallback: task.fallbackNormalCount,
                    suspect: task.suspectExcludedCount,
                  })}
                </span>
              ) : null}
            </div>

            {task?.recheckAfterWrite ? (
              <p className={styles.recheck} data-proxy-replacement-recheck={true}>
                {t('proxy_replacement.inspection_recheck')}
              </p>
            ) : null}
            {unavailable ? (
              <div className={styles.unavailableRecovery}>
                <p className={styles.error}>{t('proxy_replacement.pool_unavailable')}</p>
                {task.suspectExcludedCount > 0 ? (
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void handleRestoreSuspectsAndRetry()}
                    loading={restoringSuspects}
                    disabled={busy || !managerRequestScope}
                  >
                    {t('proxy_replacement.restore_suspects_and_retry', {
                      count: task.suspectExcludedCount,
                    })}
                  </Button>
                ) : null}
              </div>
            ) : null}
            {terminal ? (
              <p className={styles.resultSummary}>
                {t('proxy_replacement.completed_summary', {
                  updated: task?.updatedCount ?? 0,
                  failed: task?.failedCount ?? 0,
                  skipped: task?.skippedCount ?? 0,
                  recheckSuccess: task?.recheckSuccessCount ?? 0,
                  recheckFailed: task?.recheckFailedCount ?? 0,
                })}
              </p>
            ) : null}

            <div className={styles.mappingHeader}>
              <strong>{t('proxy_replacement.mapping_title')}</strong>
              <Button
                size="sm"
                variant="secondary"
                onClick={() => void handleCopyMapping()}
                disabled={!mappingText}
              >
                <IconCopy size={14} />
                {t(copied ? 'proxy_replacement.copied' : 'proxy_replacement.copy_mapping')}
              </Button>
            </div>
            <div className={styles.mappingList}>
              {taskPage.items.map((item) => (
                <div key={item.id || `${item.target.clientKey}-${item.itemOrder}`} className={styles.mappingRow}>
                  <span title={item.target.accountEmail || item.target.fileName}>
                    {item.target.accountEmail || item.target.fileName}
                  </span>
                  <code title={item.newProxyUrl || ''}>{item.newProxyUrl || '-'}</code>
                  <span className={styles.itemStatus} data-status={item.status}>
                    {t(itemStatusKey(item.status))}
                  </span>
                  {item.writeErrorMessage || item.recheckErrorMessage ? (
                    <small className={styles.itemError}>
                      {item.writeErrorMessage || item.recheckErrorMessage}
                    </small>
                  ) : null}
                </div>
              ))}
            </div>
            {taskPage.total > taskPage.pageSize ? (
              <div className={styles.pagination}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void loadTaskPage(taskPage.task.id, taskPage.page - 1)}
                  disabled={!hasPreviousPage || loadingPage}
                >
                  {t('common.previous')}
                </Button>
                <span>{t('proxy_replacement.page_summary', { page: taskPage.page, total: taskPage.total })}</span>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void loadTaskPage(taskPage.task.id, taskPage.page + 1)}
                  disabled={!hasNextPage || loadingPage}
                >
                  {loadingPage ? <IconRefreshCw size={14} /> : null}
                  {t('common.next')}
                </Button>
              </div>
            ) : null}
          </section>
        ) : null}
      </div>
    </Modal>
  );
}
