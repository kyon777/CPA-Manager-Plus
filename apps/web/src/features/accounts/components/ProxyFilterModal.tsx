import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { IconCopy, IconSearch } from '@/components/ui/icons';
import type { AuthFileItem } from '@/types';
import {
  findMissingEnabledProxyURLs,
  normalizeProxyFilterURLs,
} from '@/features/accounts/model/proxyFilter';
import { proxyReplacementFingerprint } from '@/features/accounts/model/proxyReplacement';
import { usageServiceApi, type ProxyPoolState } from '@/services/api/usageService';
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

  const savedInput = savedURLs.join('\n');
  const savedInputKey = `${sessionID}\u0000${savedInput}`;
  const input = inputOverride?.key === savedInputKey ? inputOverride.value : savedInput;

  const inputURLs = useMemo(() => parseInput(input), [input]);
  const enabledCount = Math.max(0, enabledCredentialCount);
  const poolStateByFingerprint = useMemo(
    () => new Map(poolStates.map((state) => [state.fingerprint, state] as const)),
    [poolStates]
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

  useEffect(() => {
    let active = true;
    setPoolStates([]);
    setPoolError('');
    setRestoringFingerprint('');
    if (!open || !managerRequestScope) return () => {
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

  const resultText = missingURLs?.join('\n') ?? '';
  const busy = loading || saving || Boolean(restoringFingerprint);

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
          <Button variant="secondary" onClick={() => void handleSave()} disabled={busy} loading={saving}>
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
        {suspectedSavedURLs.length > 0 ? (
          <section className={styles.poolState} aria-live="polite">
            <strong>{t('accounts.proxy_pool_suspect_count', { count: suspectedSavedURLs.length })}</strong>
            <p>{t('accounts.proxy_pool_suspect_hint')}</p>
            <div className={styles.poolStateList}>
              {suspectedSavedURLs.map(({ url, fingerprint, state }) => (
                <div
                  key={fingerprint}
                  className={styles.poolStateRow}
                  data-proxy-pool-suspect={fingerprint}
                >
                  <code title={url}>{url}</code>
                  <span>{t(`accounts.proxy_pool_reason_${state.lastFailureReason || 'unknown'}`)}</span>
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
        {missingURLs !== null ? (
          <section className={styles.result} aria-live="polite">
            <div className={styles.resultHeader}>
              <strong>{t('accounts.proxy_filter_result_count', { count: missingURLs.length })}</strong>
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
