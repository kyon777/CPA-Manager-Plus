import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import type { AuthJsonExportFailedItem } from '@/services/api/usageService';
import styles from './AuthJsonExportResultModal.module.scss';

type AuthJsonExportResultModalProps = {
  open: boolean;
  requested: number;
  exported: number;
  failed: readonly AuthJsonExportFailedItem[];
  // Present only after the browser clipboard write failed. Keeping this value
  // optional ensures ordinary partial-failure reporting never retains raw auth
  // JSON in component state.
  fallbackJson?: string;
  onClose: () => void;
  onRetryCopy?: () => void;
  onDownload?: () => void;
};

export function AuthJsonExportResultModal({
  open,
  requested,
  exported,
  failed,
  fallbackJson,
  onClose,
  onRetryCopy,
  onDownload,
}: AuthJsonExportResultModalProps) {
  const { t } = useTranslation();
  const hasFallbackJson = Boolean(fallbackJson);

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('auth_files.export_result_title')}
      width={680}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            {t('common.close')}
          </Button>
          {hasFallbackJson && onRetryCopy ? (
            <Button variant="secondary" onClick={onRetryCopy}>
              {t('auth_files.export_copy_retry')}
            </Button>
          ) : null}
          {hasFallbackJson && onDownload ? (
            <Button onClick={onDownload}>{t('auth_files.export_download_json')}</Button>
          ) : null}
        </>
      }
    >
      <div className={styles.body}>
        <p className={styles.summary}>
          {t('auth_files.export_result_summary', { exported, requested })}
        </p>
        {hasFallbackJson ? (
          <>
            <p className={styles.hint}>{t('auth_files.export_clipboard_fallback_hint')}</p>
            <textarea
              className={styles.textarea}
              value={fallbackJson}
              readOnly
              spellCheck={false}
              aria-label={t('auth_files.export_fallback_json_label')}
              data-auth-json-export-fallback
            />
          </>
        ) : null}
        {failed.length > 0 ? (
          <section className={styles.failures} aria-live="polite">
            <strong>{t('auth_files.export_failure_count', { count: failed.length })}</strong>
            <ul>
              {failed.map((item, index) => (
                <li
                  key={`${item.clientKey}:${item.fileName}:${index}`}
                  data-auth-json-export-failure={item.clientKey}
                >
                  <code>{item.fileName}</code>
                  <span>
                    {t(`auth_files.export_error_${item.code}`, { defaultValue: item.code })}
                  </span>
                </li>
              ))}
            </ul>
          </section>
        ) : null}
      </div>
    </Modal>
  );
}
