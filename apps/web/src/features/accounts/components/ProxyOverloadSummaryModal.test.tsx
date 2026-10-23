import { act, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import type { ProxyOverloadSummaryResponse } from '@/services/api/usageService';
import { ProxyOverloadSummaryModal } from './ProxyOverloadSummaryModal';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      typeof values?.count === 'number' ? `${key}:${values.count}` : key,
  }),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: (props: { children: ReactNode; footer?: ReactNode }) => (
    <div>
      {props.children}
      {props.footer}
    </div>
  ),
}));

const result: ProxyOverloadSummaryResponse = {
  generated_at_ms: 1_792_752_000_000,
  window_start_ms: 1_792_492_800_000,
  active_account_count: 4,
  missing_proxy_account_count: 1,
  proxy_count: 3,
  overloaded_account_count: 2,
  proxies: [
    {
      proxy_url: 'http://loaded.example:8080',
      active_account_count: 2,
      overloaded_account_count: 2,
      non_overloaded_account_count: 0,
      overloaded_rate: 1,
    },
    {
      proxy_url: 'http://healthy.example:8080',
      active_account_count: 2,
      overloaded_account_count: 0,
      non_overloaded_account_count: 2,
      overloaded_rate: 0,
    },
    {
      proxy_url: 'http://missing-load.example:8080',
      active_account_count: 1,
      overloaded_account_count: 0,
      non_overloaded_account_count: 1,
      overloaded_rate: 0,
    },
  ],
};

const hasText = (value: unknown, text: string): boolean => {
  if (typeof value === 'string') return value.includes(text);
  if (value && typeof value === 'object' && 'props' in value) {
    return hasText((value as { props?: { children?: unknown } }).props?.children, text);
  }
  return Array.isArray(value) && value.some((entry) => hasText(entry, text));
};

const findButton = (renderer: ReactTestRenderer, key: string) => {
  const button = renderer.root
    .findAllByType(Button)
    .find((node) => hasText(node.props.children, key));
  if (!button) throw new Error(`Button ${key} not found`);
  return button;
};

const renderModal = (overrides: Partial<React.ComponentProps<typeof ProxyOverloadSummaryModal>> = {}) =>
  create(
    <ProxyOverloadSummaryModal
      open
      loading={false}
      refreshing={false}
      result={result}
      error=""
      onClose={() => {}}
      onRefresh={() => {}}
      onCopy={vi.fn().mockResolvedValue(undefined)}
      {...overrides}
    />
  );

describe('ProxyOverloadSummaryModal', () => {
  it('renders aggregate cards and default zero-overload-first rows', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = renderModal();
    });

    const rows = renderer!.root.findAllByProps({ 'data-proxy-overload-row': true });
    expect(rows.map((row) => row.props['data-proxy-url'])).toEqual([
      'http://healthy.example:8080',
      'http://missing-load.example:8080',
      'http://loaded.example:8080',
    ]);
    expect(
      renderer!.root.findAll((node) => hasText(node.props.children, 'accounts.proxy_overload_active:4'))
        .length
    ).toBeGreaterThan(0);
    renderer!.unmount();
  });

  it('filters rows locally without asking the parent to refresh', () => {
    const onRefresh = vi.fn();
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = renderModal({ onRefresh });
    });

    act(() => {
      findButton(renderer!, 'accounts.proxy_overload_filter_overloaded').props.onClick();
    });
    const rows = renderer!.root.findAllByProps({ 'data-proxy-overload-row': true });
    expect(rows.map((row) => row.props['data-proxy-url'])).toEqual([
      'http://loaded.example:8080',
    ]);
    expect(onRefresh).not.toHaveBeenCalled();
    renderer!.unmount();
  });

  it('selects visible rows and copies LF-only proxy URLs', async () => {
    const onCopy = vi.fn().mockResolvedValue(undefined);
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = renderModal({ onCopy });
    });

    await act(async () => {
      findButton(renderer!, 'accounts.proxy_overload_select_visible').props.onClick();
    });
    await act(async () => {
      findButton(renderer!, 'accounts.proxy_overload_copy_selected').props.onClick();
    });
    expect(onCopy).toHaveBeenCalledWith(
      'http://healthy.example:8080\nhttp://missing-load.example:8080\nhttp://loaded.example:8080'
    );
    renderer!.unmount();
  });

  it('retains the last complete table while rendering a refresh failure', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = renderModal({ refreshing: true, error: 'safe refresh failure' });
    });

    expect(
      renderer!.root.findAll((node) => hasText(node.props.children, 'safe refresh failure')).length
    ).toBeGreaterThan(0);
    expect(renderer!.root.findAllByProps({ 'data-proxy-overload-row': true })).toHaveLength(3);
    renderer!.unmount();
  });

  it('keeps an initial manual refresh visibly loading when no prior result exists', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = renderModal({ result: null, refreshing: true });
    });

    expect(
      renderer!.root.findAll((node) => hasText(node.props.children, 'accounts.proxy_overload_loading'))
        .length
    ).toBeGreaterThan(0);
    renderer!.unmount();
  });

  it('renders the empty state and missing-proxy total', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = renderModal({
        result: {
          ...result,
          active_account_count: 2,
          missing_proxy_account_count: 2,
          proxy_count: 0,
          overloaded_account_count: 0,
          proxies: [],
        },
      });
    });

    expect(
      renderer!.root.findAll((node) => hasText(node.props.children, 'accounts.proxy_overload_empty'))
        .length
    ).toBeGreaterThan(0);
    expect(
      renderer!.root.findAll((node) => hasText(node.props.children, 'accounts.proxy_overload_missing:2'))
        .length
    ).toBeGreaterThan(0);
    renderer!.unmount();
  });
});
