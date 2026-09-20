import type { TFunction } from 'i18next';
import type { CodexInspectionQuotaWindow } from '@/features/monitoring/codexInspection';
import { formatInspectionQuotaResetLabel } from '@/features/monitoring/model/codexInspectionPresentation';
import { formatQuotaResetTime, isValidQuotaResetAtMs } from '@/utils/quota/formatters';
import styles from '../CodexInspectionPage.module.scss';

export type CodexInspectionQuotaWindowView = {
  id: string;
  labelKey: string;
  labelParams?: Record<string, string | number>;
  usedPercent?: number | null;
  resetLabel?: string;
  resetAtMs?: number | null;
  resetAccuracy?: CodexInspectionQuotaWindow['resetAccuracy'];
};

export type CodexInspectionCreditsView = {
  observed?: boolean;
  balance?: string | number | null;
  hasCredits?: boolean | null;
  unlimited?: boolean | null;
};

type CodexInspectionQuotaWindowsProps = {
  windows?: readonly CodexInspectionQuotaWindowView[] | null;
  fallbackUsedPercent?: number | null;
  credits?: CodexInspectionCreditsView | null;
  t: TFunction;
};

const clampPercent = (value: number) => Math.min(100, Math.max(0, value));

const normalizePercent = (value?: number | null) =>
  typeof value === 'number' && Number.isFinite(value) ? clampPercent(value) : null;

const formatRemainingPercent = (value: number | null) => {
  if (value === null) return '--';
  return `${Number.isInteger(value) ? value.toFixed(0) : value.toFixed(1)}%`;
};

const getQuotaFillClass = (remainingPercent: number | null) => {
  if (remainingPercent === null) return styles.quotaWindowBarFillMedium;
  if (remainingPercent >= 70) return styles.quotaWindowBarFillHigh;
  if (remainingPercent >= 30) return styles.quotaWindowBarFillMedium;
  return styles.quotaWindowBarFillLow;
};

const formatQuotaLabel = (window: CodexInspectionQuotaWindowView, t: TFunction) =>
  t(window.labelKey, window.labelParams ?? {});

const formatCreditsLabel = (credits?: CodexInspectionCreditsView | null) => {
  if (!credits?.observed) return null;
  if (credits.balance !== null && credits.balance !== undefined) {
    const balance = String(credits.balance).trim();
    if (balance) return `Credits ${balance}`;
  }
  if (credits.unlimited === true) return 'Credits 无限';
  if (credits.hasCredits === true) return 'Credits 可用';
  return 'Credits --';
};

export function CodexInspectionQuotaWindows({
  windows,
  fallbackUsedPercent,
  credits,
  t,
}: CodexInspectionQuotaWindowsProps) {
  const normalizedFallbackUsedPercent = normalizePercent(fallbackUsedPercent);
  const knownRows = (windows ?? []).flatMap((window) => {
    const usedPercent = normalizePercent(window.usedPercent);
    return usedPercent === null
      ? []
      : [
          {
            id: window.id,
            label: formatQuotaLabel(window, t),
            resetLabel: window.resetLabel,
            resetAtMs: window.resetAtMs,
            resetAccuracy: window.resetAccuracy,
            usedPercent,
          },
        ];
  });
  const rows =
    knownRows.length > 0
      ? knownRows
      : normalizedFallbackUsedPercent === null
        ? []
        : [
            {
              id: 'overall',
              label: t('monitoring.codex_inspection_used_percent'),
              resetLabel: '',
              resetAtMs: null,
              resetAccuracy: undefined,
              usedPercent: normalizedFallbackUsedPercent,
            },
          ];
  const creditsLabel = formatCreditsLabel(credits);
  const creditsRowIndex =
    ['monthly', 'weekly', 'long']
      .map((id) => rows.findIndex((row) => row.id === id))
      .find((index) => index >= 0) ?? 0;

  if (rows.length === 0) {
    return (
      <div className={styles.quotaWindowEmpty}>
        <span className={styles.quotaWindowUnavailable}>
          {t('monitoring.codex_inspection_quota_unavailable')}
        </span>
        {creditsLabel ? (
          <span className={styles.quotaWindowCredits} data-inspection-credits>
            {creditsLabel}
          </span>
        ) : null}
        <span className={styles.quotaWindowPlaceholderBar} aria-hidden="true" />
      </div>
    );
  }

  return (
    <div className={styles.quotaWindowList}>
      {rows.map((row, rowIndex) => {
        const usedPercent = normalizePercent(row.usedPercent);
        const remainingPercent = usedPercent === null ? null : clampPercent(100 - usedPercent);
        const resetTime = isValidQuotaResetAtMs(row.resetAtMs)
          ? formatQuotaResetTime(row.resetAtMs)
          : formatInspectionQuotaResetLabel(row.resetLabel);
        const resetLabel = resetTime
          ? t('monitoring.codex_inspection_quota_reset', { time: resetTime })
          : '';

        return (
          <div key={row.id} className={styles.quotaWindowRow}>
            <div className={styles.quotaWindowHeader}>
              <span className={styles.quotaWindowLabel}>{row.label}</span>
              <span className={styles.quotaWindowMeta}>
                <span className={styles.quotaWindowValue}>
                  {t('monitoring.codex_inspection_quota_remaining', {
                    percent: formatRemainingPercent(remainingPercent),
                  })}
                </span>
                {rowIndex === creditsRowIndex && creditsLabel ? (
                  <span className={styles.quotaWindowCredits} data-inspection-credits>
                    {creditsLabel}
                  </span>
                ) : null}
              </span>
            </div>
            <div className={styles.quotaWindowBar} aria-hidden="true">
              <span
                className={`${styles.quotaWindowBarFill} ${getQuotaFillClass(remainingPercent)}`}
                style={{ width: `${remainingPercent ?? 0}%` }}
              />
            </div>
            {resetLabel ? <span className={styles.quotaWindowReset}>{resetLabel}</span> : null}
          </div>
        );
      })}
    </div>
  );
}
