import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal } from '@/components/ui/Modal';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import {
  DEFAULT_AUTH_JSON_FILE_NAME,
  type AuthJsonInputType,
} from '@/features/authFiles/sessionAuthConverter';
import styles from './AuthJsonPasteModal.module.scss';

type AuthJsonPasteModalProps = {
  open: boolean;
  saving: boolean;
  disabled?: boolean;
  onClose: () => void;
  onSave: (type: AuthJsonInputType, fileName: string, jsonText: string) => Promise<void>;
};

export function AuthJsonPasteModal({
  open,
  saving,
  disabled = false,
  onClose,
  onSave,
}: AuthJsonPasteModalProps) {
  const { t } = useTranslation();
  const [type, setType] = useState<AuthJsonInputType>('cpa');
  const [jsonText, setJsonText] = useState('');
  const [error, setError] = useState('');

  const resetForm = () => {
    setType('cpa');
    setJsonText('');
    setError('');
  };

  const handleClose = () => {
    resetForm();
    onClose();
  };

  const options = useMemo(
    () => [
      { value: 'cpa', label: t('auth_files.paste_type_cpa') },
      { value: 'session', label: t('auth_files.paste_type_session') },
      { value: 'sub2api', label: t('auth_files.paste_type_sub2api') },
    ],
    [t]
  );

  const pastePlaceholderKey =
    type === 'session'
      ? 'auth_files.paste_session_placeholder'
      : type === 'sub2api'
        ? 'auth_files.paste_sub2api_placeholder'
        : 'auth_files.paste_cpa_placeholder';
  const pasteHintKey =
    type === 'session'
      ? 'auth_files.paste_session_hint'
      : type === 'sub2api'
        ? 'auth_files.paste_sub2api_hint'
        : 'auth_files.paste_cpa_hint';
  const canSave = !disabled && !saving && Boolean(jsonText.trim());

  const handleSave = async () => {
    if (saving || disabled) return;

    if (!jsonText.trim()) {
      setError(t('auth_files.paste_error_json_required'));
      return;
    }

    setError('');
    try {
      await onSave(type, DEFAULT_AUTH_JSON_FILE_NAME, jsonText);
      resetForm();
    } catch (err) {
      setError(err instanceof Error ? err.message : t('notification.save_failed'));
    }
  };

  return (
    <Modal
      open={open}
      onClose={handleClose}
      title={t('auth_files.paste_title')}
      width={640}
      closeDisabled={saving}
      footer={
        <>
          <Button variant="secondary" onClick={handleClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={handleSave} loading={saving} disabled={!canSave}>
            {t('auth_files.paste_save_button')}
          </Button>
        </>
      }
    >
      <div className={styles.authJsonPasteModal}>
        {error && <div className={styles.prefixProxyError}>{error}</div>}
        <div className={styles.formGroup}>
          <label>{t('auth_files.paste_type_label')}</label>
          <Select
            value={type}
            options={options}
            onChange={(value) => setType(value as AuthJsonInputType)}
            ariaLabel={t('auth_files.paste_type_label')}
            disabled={saving || disabled}
          />
        </div>
        <div className={styles.formGroup}>
          <label htmlFor="auth-json-paste-content">{t('auth_files.paste_json_label')}</label>
          <textarea
            id="auth-json-paste-content"
            className={styles.authJsonPasteTextarea}
            value={jsonText}
            onChange={(event) => setJsonText(event.target.value)}
            disabled={saving || disabled}
            spellCheck={false}
            placeholder={t(pastePlaceholderKey)}
          />
        </div>
        <p className={styles.authJsonPasteHint}>{t(pasteHintKey)}</p>
      </div>
    </Modal>
  );
}
