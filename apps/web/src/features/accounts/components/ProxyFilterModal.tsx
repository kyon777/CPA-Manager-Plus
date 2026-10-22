import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { IconCopy, IconRefreshCw, IconSearch } from '@/components/ui/icons';
import type { AuthFileItem } from '@/types';
import {
  findMissingEnabledProxyURLs,
  normalizeProxyFilterURLs,
} from '@/features/accounts/model/proxyFilter';
import { proxyReplacementFingerprint } from '@/features/accounts/model/proxyReplacement';
import {
  usageServiceApi,
  type ProxyExitIPCheckResult,
  type ProxyPoolState,
} from '@/services/api/usageService';
import styles from './ProxyFilterModal.module.scss';

type ProxyFilterModalProps = {
  open: boolean;
  sessionID: number;
  files: readonly AuthFileItem[];
  enabledProxyURLs: readonly string[];
  enabledCredentialCount: number;
  credentialsLoading: boolean;
  credentialsError: string;
  loading: boolean;
  saving: boolean;
  savedURLs: readonly string[];
  error: string;
  managerRequestScope?: {
    apiBase: string;
    managementKey?: string;
  };
  onClose: () => void;
  onSave: (urls: string[]) => Promise<string[]>;
  onCopy: (text: string) => Promise<void>;
};

const parseInput = (value: string) => normalizeProxyFilterURLs(value.split(/\r?\n/));

export function ProxyFilterModal({
  open,
  sessionID,
  files,
  enabledProxyURLs,
  enabledCredentialCount,
  credentialsLoading,
  credentialsError,
  loading,
  saving,
  savedURLs,
  error,
  managerRequestScope,
  onClose,
  onSave,
  onCopy,
}: ProxyFilterModalProps) {
  const { t } = useTranslation();
  const [inputOverride, setInputOverride] = useState<{ key: string; value: string } | null>(null);
  const [missingURLs, setMissingURLs] = useState<string[] | null>(null);
  const [poolStates, setPoolStates] = useState<ProxyPoolState[]>([]);
  const [poolError, setPoolError] = useState('');
  const [restoringFingerprint, setRestoringFingerprint] = useState('');
  const [verificationBusy, setVerificationBusy] = useState(false);
  const [verificationTotal, setVerificationTotal] = useState(0);
  const [verificationResults, setVerificationResults] = useState<ProxyExitIPCheckResult[]>([]);

  const savedInput = savedURLs.join('\n');
  const savedInputKey = `${sessionID}\u0000${savedInput}`;
  const input = inputOverride?.key === savedInputKey ? inputOverride.value : savedInput;

  const inputURLs = useMemo(() => parseInput(input), [input]);
  const enabledCount = Math.max(0, enabledCredentialCount);
  const poolStateByFingerprint = useMemo(
    () => new Map(poolStates.map((state) => [state.fingerprint, state] as const)),
    [poolStates]
  );
  const savedURLByFingerprint = useMemo(
    () =>
      new Map(
        normalizeProxyFilterURLs(savedURLs).map(
          (url) => [proxyReplacementFingerprint(url), url] as const
        )
      ),
    [savedURLs]
  );
  const pendingSavedURLs = useMemo(
    () =>
      normalizeProxyFilterURLs(savedURLs).flatMap((url) => {
        const fingerprint = proxyReplacementFingerprint(url);
        const state = poolStateByFingerprint.get(fingerprint);
        return state?.status === 'pending_verification' ? [{ url, fingerprint, state }] : [];
      }),
    [poolStateByFingerprint, savedURLs]
  );
  const suspectedSavedURLs = useMemo(
    () =>
      normalizeProxyFilterURLs(savedURLs).flatMap((url) => {
        const fingerprint = proxyReplacementFingerprint(url);
        const state = poolStateByFingerprint.get(fingerprint);
        return state?.status === 'suspect' ? [{ url, fingerprint, state }] : [];
      }),
    [poolStateByFingerprint, savedURLs]
  );
  const verificationCandidates = useMemo(
    () =>
      Array.from(
        new Set([...pendingSavedURLs, ...suspectedSavedURLs].map(({ fingerprint }) => fingerprint))
      ),
    [pendingSavedURLs, suspectedSavedURLs]
  );

  useEffect(() => {
    let active = true;
    setPoolStates([]);
    setPoolError('');
    setRestoringFingerprint('');
    setVerificationBusy(false);
    setVerificationTotal(0);
    setVerificationResults([]);
    if (!open || !managerRequestScope)
      return () => {
        active = false;
      };
    void usageServiceApi
      .listProxyPoolStates(managerRequestScope.apiBase, managerRequestScope.managementKey)
      .then((response) => {
        if (!active) return;
        setPoolStates(Array.isArray(response.items) ? response.items : []);
      })
      .catch((loadError) => {
        if (!active) return;
        setPoolError(loadError instanceof Error ? loadError.message : String(loadError));
      });
    return () => {
      active = false;
    };
  }, [managerRequestScope, open, sessionID]);

  const handleSave = async () => {
    try {
      const persisted = await onSave(inputURLs);
      const persistedInput = persisted.join('\n');
      setInputOverride({ key: `${sessionID}\u0000${persistedInput}`, value: persistedInput });
      setMissingURLs(null);
    } catch {
      // The parent owns the transport error state rendered below the input.
    }
  };

  const handleFilter = () => {
    if (credentialsLoading || credentialsError) return;
    setMissingURLs(findMissingEnabledProxyURLs(inputURLs, files, enabledProxyURLs));
  };

  const handleRestore = async (fingerprint: string) => {
    if (!managerRequestScope || !fingerprint) return;
    setRestoringFingerprint(fingerprint);
    setPoolError('');
    try {
      const restored = await usageServiceApi.restoreProxyPoolState(
        managerRequestScope.apiBase,
        managerRequestScope.managementKey,
        fingerprint
      );
      setPoolStates((current) => {
        const next = current.filter((entry) => entry.fingerprint !== restored.fingerprint);
        return [...next, restored];
      });
    } catch (restoreError) {
      setPoolError(restoreError instanceof Error ? restoreError.message : String(restoreError));
    } finally {
      setRestoringFingerprint('');
    }
  };

  const handleVerifyExitIPs = async (fingerprints: readonly string[]) => {
    if (!managerRequestScope || verificationBusy) return;
    const selected = Array.from(
      new Set(fingerprints.map((fingerprint) => fingerprint.trim()).filter(Boolean))
    );
    if (selected.length === 0) return;
    setVerificationBusy(true);
    setVerificationTotal(selected.length);
    setVerificationResults([]);
    setPoolError('');
    try {
      const batch = await usageServiceApi.verifyProxyPoolExitIPs(
        managerRequestScope.apiBase,
        managerRequestScope.managementKey,
        selected
      );
      const results = Array.isArray(batch.results) ? batch.results : [];
      setVerificationResults(results);
      const refreshed = await usageServiceApi.listProxyPoolStates(
        managerRequestScope.apiBase,
        managerRequestScope.managementKey
      );
      setPoolStates(Array.isArray(refreshed.items) ? refreshed.items : []);
    } catch (verificationError) {
      setPoolError(
        verificationError instanceof Error ? verificationError.message : String(verificationError)
      );
    } finally {
      setVerificationBusy(false);
    }
  };

  const resultText = missingURLs?.join('\n') ?? '';
  const busy = loading || saving || Boolean(restoringFingerprint) || verificationBusy;

  return (
    <Modal
      open={open}
      onClose={onClose}
      closeDisabled={busy}
      title={t('accounts.proxy_filter_title')}
      width={680}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => void handleSave()}
            disabled={busy}
            loading={saving}
          >
            {t('accounts.proxy_filter_save')}
          </Button>
          <Button
            onClick={handleFilter}
            disabled={busy || credentialsLoading || Boolean(credentialsError)}
            title={
              credentialsLoading
                ? t('accounts.proxy_filter_credentials_loading')
                : credentialsError || undefined
            }
          >
            <IconSearch size={15} />
            {t('accounts.proxy_filter_run')}
          </Button>
        </>
      }
    >
      <div className={styles.body}>
        <p className={styles.hint}>{t('accounts.proxy_filter_hint')}</p>
        <label className={styles.label} htmlFor="proxy-filter-input">
          {t('accounts.proxy_filter_input_label')}
        </label>
        <textarea
          id="proxy-filter-input"
          className={styles.textarea}
          value={input}
          onChange={(event) => {
            setInputOverride({ key: savedInputKey, value: event.target.value });
            setMissingURLs(null);
          }}
          disabled={busy}
          spellCheck={false}
          placeholder={t('accounts.proxy_filter_input_placeholder')}
        />
        <div className={styles.meta}>
          <span>{t('accounts.proxy_filter_input_count', { count: inputURLs.length })}</span>
          <span>{t('accounts.proxy_filter_enabled_count', { count: enabledCount })}</span>
        </div>
        {loading ? <p className={styles.loading}>{t('accounts.proxy_filter_loading')}</p> : null}
        {error ? <div className={styles.error}>{error}</div> : null}
        {credentialsLoading ? (
          <p className={styles.loading}>{t('accounts.proxy_filter_credentials_loading')}</p>
        ) : null}
        {credentialsError ? <div className={styles.error}>{credentialsError}</div> : null}
        {poolError ? <div className={styles.error}>{poolError}</div> : null}
        {verificationCandidates.length > 0 ? (
          <div className={styles.poolStateActions}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => void handleVerifyExitIPs(verificationCandidates)}
              loading={verificationBusy}
              disabled={busy}
            >
              <IconRefreshCw size={14} />
              {t('accounts.proxy_pool_verify_all', { count: verificationCandidates.length })}
            </Button>
          </div>
        ) : null}
        {verificationBusy ? (
          <p className={styles.verificationProgress} aria-live="polite">
            {t('accounts.proxy_pool_verify_progress', { count: verificationTotal })}
          </p>
        ) : null}
        {pendingSavedURLs.length > 0 ? (
          <section className={styles.pendingPoolState} aria-live="polite">
            <strong>
              {t('accounts.proxy_pool_pending_count', { count: pendingSavedURLs.length })}
            </strong>
            <p>{t('accounts.proxy_pool_pending_hint')}</p>
            <div className={styles.poolStateList}>
              {pendingSavedURLs.map(({ url, fingerprint, state }) => (
                <div
                  key={fingerprint}
                  className={styles.poolStateRow}
                  data-proxy-pool-pending={fingerprint}
                >
                  <code title={url}>{url}</code>
                  <span>
                    {t(`accounts.proxy_pool_reason_${state.lastFailureReason || 'unknown'}`)}
                  </span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void handleVerifyExitIPs([fingerprint])}
                    loading={verificationBusy}
                    disabled={busy}
                  >
                    {t('accounts.proxy_pool_verify_one')}
                  </Button>
                </div>
              ))}
            </div>
          </section>
        ) : null}
        {suspectedSavedURLs.length > 0 ? (
          <section className={styles.poolState} aria-live="polite">
            <strong>
              {t('accounts.proxy_pool_suspect_count', { count: suspectedSavedURLs.length })}
            </strong>
            <p>{t('accounts.proxy_pool_suspect_hint')}</p>
            <div className={styles.poolStateList}>
              {suspectedSavedURLs.map(({ url, fingerprint, state }) => (
                <div
                  key={fingerprint}
                  className={styles.poolStateRow}
                  data-proxy-pool-suspect={fingerprint}
                >
                  <code title={url}>{url}</code>
                  <span>
                    {t(`accounts.proxy_pool_reason_${state.lastFailureReason || 'unknown'}`)}
                  </span>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void handleVerifyExitIPs([fingerprint])}
                    loading={verificationBusy}
                    disabled={busy}
                  >
                    {t('accounts.proxy_pool_verify_one')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => void handleRestore(fingerprint)}
                    loading={restoringFingerprint === fingerprint}
                    disabled={busy}
                  >
                    {t('accounts.proxy_pool_restore')}
                  </Button>
                </div>
              ))}
            </div>
          </section>
        ) : null}
        {verificationResults.length > 0 ? (
          <section className={styles.verificationResults} aria-live="polite">
            <strong>
              {t('accounts.proxy_pool_verify_result_count', { count: verificationResults.length })}
            </strong>
            <div className={styles.verificationResultList}>
              {verificationResults.map((result) => (
                <div
                  key={result.fingerprint}
                  className={styles.verificationResultRow}
                  data-proxy-exit-ip-result={result.fingerprint}
                >
                  <code title={savedURLByFingerprint.get(result.fingerprint)}>
                    {savedURLByFingerprint.get(result.fingerprint) || result.fingerprint}
                  </code>
                  <span className={styles[`verification${result.outcome}`]}>
                    {t(`accounts.proxy_pool_outcome_${result.outcome}`)}
                  </span>
                  {result.exitIp ? (
                    <span>{t('accounts.proxy_pool_exit_ip', { ip: result.exitIp })}</span>
                  ) : null}
                  {typeof result.latencyMs === 'number' ? (
                    <span>{t('accounts.proxy_pool_latency', { ms: result.latencyMs })}</span>
                  ) : null}
                  {result.reason ? (
                    <span>{t(`accounts.proxy_pool_reason_${result.reason}`)}</span>
                  ) : null}
                </div>
              ))}
            </div>
          </section>
        ) : null}
        {missingURLs !== null ? (
          <section className={styles.result} aria-live="polite">
            <div className={styles.resultHeader}>
              <strong>
                {t('accounts.proxy_filter_result_count', { count: missingURLs.length })}
              </strong>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void onCopy(resultText)}
                disabled={missingURLs.length === 0}
              >
                <IconCopy size={14} />
                {t('accounts.proxy_filter_copy')}
              </Button>
            </div>
            <textarea
              className={styles.resultTextarea}
              value={resultText}
              readOnly
              aria-label={t('accounts.proxy_filter_result_label')}
              placeholder={t('accounts.proxy_filter_empty')}
            />
          </section>
        ) : null}
      </div>
    </Modal>
  );
}
