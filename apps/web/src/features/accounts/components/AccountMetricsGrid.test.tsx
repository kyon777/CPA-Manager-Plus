import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { AccountMetricsGrid } from './AccountMetricsGrid';
import { SummaryCard } from '@/features/monitoring/components/MonitoringShared';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

describe('AccountMetricsGrid', () => {
  it('renders the six credential status cards in operational order', () => {
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <AccountMetricsGrid
          metrics={{
            total: 12,
            available: 6,
            needsAttention: 2,
            quotaRisk: 1,
            disabled: 2,
            unconfirmed: 1,
            needsInspectionAction: 3,
          }}
        />
      );
    });

    const cards = renderer!.root.findAll(
      (node) => typeof node.props['data-summary-icon'] === 'string'
    );
    expect(cards.map((card) => card.props['data-summary-icon'])).toEqual([
      'credential',
      'available',
      'attention',
      'quota-risk',
      'disabled',
      'unconfirmed',
    ]);
    expect(cards.map((card) => card.findByType('strong').children.join(''))).toEqual([
      '12',
      '6',
      '2',
      '1',
      '2',
      '1',
    ]);
    expect(renderer!.root.findAllByType('svg')).toHaveLength(12);
    expect(renderer!.root.findByProps({ 'aria-label': 'accounts.metrics_label' })).toBeTruthy();
  });

  it('renders global health labels for the server summary while it warms', () => {
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <AccountMetricsGrid
          metrics={{
            total: 426,
            available: 292,
            needsAttention: 0,
            quotaRisk: 5,
            disabled: 0,
            unconfirmed: 45,
            needsInspectionAction: 0,
          }}
          globalSummaryPending
        />
      );
    });

    const cards = renderer!.root.findAllByType(SummaryCard);
    expect(cards.map((card) => card.props.label)).toEqual([
      'accounts.metric_total',
      'accounts.metric_available',
      'accounts.metric_attention',
      'accounts.metric_quota_risk',
      'accounts.metric_disabled',
      'accounts.metric_unconfirmed',
    ]);
    expect(cards[5]?.props.meta).toContain('accounts.metric_global_summary_warming');
  });
});
