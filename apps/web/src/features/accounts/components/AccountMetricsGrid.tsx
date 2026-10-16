import { useTranslation } from 'react-i18next';
import type { AccountMetrics } from '@/features/accounts/model/accountRows';
import {
  SummaryCard,
  type SummaryCardAccent,
  type SummaryCardIcon,
} from '@/features/monitoring/components/MonitoringShared';
import styles from '../AccountsPage.module.scss';

interface AccountMetricsGridProps {
  metrics: AccountMetrics;
  /** The list response contains one page, not a global health aggregate. */
  pageScopedHealth?: boolean;
  /** The Manager returned an exact total for the active enabled filter. */
  enabledSummary?: boolean;
}

export function AccountMetricsGrid({
  metrics,
  pageScopedHealth = false,
  enabledSummary = false,
}: AccountMetricsGridProps) {
  const { t } = useTranslation();
  const pageScoped = (labelKey: string, metaKey: string) =>
    pageScopedHealth
      ? {
          label: t('accounts.metric_current_page', { label: t(labelKey) }),
          meta: t('accounts.metric_current_page_meta'),
        }
      : { label: t(labelKey), meta: t(metaKey) };
  const available = enabledSummary
    ? {
        label: t('accounts.metric_enabled'),
        meta: t('accounts.metric_enabled_meta'),
      }
    : pageScoped('accounts.metric_available', 'accounts.metric_available_meta');
  const attention = pageScoped('accounts.metric_attention', 'accounts.metric_attention_meta');
  const quotaRisk = pageScoped('accounts.metric_quota_risk', 'accounts.metric_quota_risk_meta');
  const disabled = pageScoped('accounts.metric_disabled', 'accounts.metric_disabled_meta');
  const unconfirmed = pageScoped('accounts.metric_unconfirmed', 'accounts.metric_unconfirmed_meta');
  const cards = [
    {
      key: 'total',
      label: t('accounts.metric_total'),
      value: metrics.total,
      meta: t('accounts.metric_total_meta'),
      icon: 'credential' as const,
      accent: 'blue' as const,
    },
    {
      key: 'available',
      label: available.label,
      value: metrics.available,
      meta: available.meta,
      icon: 'available' as const,
      accent: 'green' as const,
    },
    {
      key: 'attention',
      label: attention.label,
      value: metrics.needsAttention,
      meta: attention.meta,
      icon: 'attention' as const,
      accent: 'red' as const,
    },
    {
      key: 'quota-risk',
      label: quotaRisk.label,
      value: metrics.quotaRisk,
      meta: quotaRisk.meta,
      icon: 'quota-risk' as const,
      accent: 'amber' as const,
    },
    {
      key: 'disabled',
      label: disabled.label,
      value: metrics.disabled,
      meta: disabled.meta,
      icon: 'disabled' as const,
      accent: 'neutral' as const,
    },
    {
      key: 'unconfirmed',
      label: unconfirmed.label,
      value: metrics.unconfirmed,
      meta: unconfirmed.meta,
      icon: 'unconfirmed' as const,
      accent: 'violet' as const,
    },
  ] satisfies Array<{
    key: string;
    label: string;
    value: number;
    meta: string;
    icon: SummaryCardIcon;
    accent: SummaryCardAccent;
  }>;

  return (
    <section className={styles.metricsSection} aria-label={t('accounts.metrics_label')}>
      <div className={styles.metricsGrid}>
        {cards.map((card) => (
          <SummaryCard
            key={card.key}
            label={card.label}
            value={String(card.value)}
            meta={card.meta}
            icon={card.icon}
            accent={card.accent}
          />
        ))}
      </div>
    </section>
  );
}
