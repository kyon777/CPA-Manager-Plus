import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { IconCopy, IconSearch } from '@/components/ui/icons';
import type { AuthFileItem } from '@/types';
import {
  findMissingEnabledProxyURLs,
  normalizeProxyFilterURLs,
} from '@/features/accounts/model/proxyFilter';
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
  onClose,
  onSave,
  onCopy,
}: ProxyFilterModalProps) {
  const { t } = useTranslation();
  const [inputOverride, setInputOverride] = useState<{ key: string; value: string } | null>(null);
  const [missingURLs, setMissingURLs] = useState<string[] | null>(null);

  const savedInput = savedURLs.join('\n');
  const savedInputKey = `${sessionID}\u0000${savedInput}`;
  const input = inputOverride?.key === savedInputKey ? inputOverride.value : savedInput;

  const inputURLs = useMemo(() => parseInput(input), [input]);
  const enabledCount = Math.max(0, enabledCredentialCount);

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

  const resultText = missingURLs?.join('\n') ?? '';
  const busy = loading || saving;

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
