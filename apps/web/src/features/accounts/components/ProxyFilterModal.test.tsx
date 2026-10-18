import { act, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import type { AuthFileItem } from '@/types';
import { proxyReplacementFingerprint } from '@/features/accounts/model/proxyReplacement';
import { ProxyFilterModal } from './ProxyFilterModal';

const mocks = vi.hoisted(() => ({
  listProxyPoolStates: vi.fn(),
  restoreProxyPoolState: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: (props: { children: ReactNode; footer?: ReactNode }) => (
    <div>
      {props.children}
      {props.footer}
    </div>
  ),
}));

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    listProxyPoolStates: mocks.listProxyPoolStates,
    restoreProxyPoolState: mocks.restoreProxyPoolState,
  },
}));

const makeFile = (overrides: Partial<AuthFileItem>): AuthFileItem => ({
  name: 'credential.json',
  ...overrides,
});

const hasText = (value: unknown, text: string): boolean => {
  if (typeof value === 'string') return value.includes(text);
  return Array.isArray(value) && value.some((entry) => hasText(entry, text));
};

const findButton = (renderer: ReactTestRenderer, key: string) => {
  const button = renderer.root
    .findAllByType(Button)
    .find((node) => hasText(node.props.children, key));
  if (!button) throw new Error(`Button ${key} not found`);
  return button;
};

describe('ProxyFilterModal', () => {
  it('shows a suspected saved proxy and restores it only after an explicit click', async () => {
    const proxyURL = 'http://suspect.example:8080/';
    mocks.listProxyPoolStates.mockResolvedValue({
      items: [
        {
          fingerprint: proxyReplacementFingerprint(proxyURL),
          status: 'suspect',
          lastFailureReason: 'transport_timeout',
          updatedAtMs: 1,
        },
      ],
    });
    mocks.restoreProxyPoolState.mockResolvedValue({
      fingerprint: proxyReplacementFingerprint(proxyURL),
      status: 'normal',
      updatedAtMs: 2,
    });
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProxyFilterModal
          open
          sessionID={91}
          files={[]}
          enabledProxyURLs={[]}
          enabledCredentialCount={0}
          credentialsError=""
          credentialsLoading={false}
          loading={false}
          saving={false}
          savedURLs={[proxyURL]}
          error=""
          managerRequestScope={{ apiBase: 'https://manager.example', managementKey: 'key' }}
          onClose={() => {}}
          onSave={vi.fn().mockResolvedValue([])}
          onCopy={vi.fn().mockResolvedValue(undefined)}
        />
      );
    });

    expect(
      renderer!.root.findByProps({ 'data-proxy-pool-suspect': proxyReplacementFingerprint(proxyURL) })
    ).toBeTruthy();
    await act(async () => {
      findButton(renderer!, 'accounts.proxy_pool_restore').props.onClick();
    });
    expect(mocks.restoreProxyPoolState).toHaveBeenCalledWith(
      'https://manager.example',
      'key',
      proxyReplacementFingerprint(proxyURL)
    );
    renderer!.unmount();
  });

  it('returns only requested URLs not used by any enabled credential', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ProxyFilterModal
          open
          sessionID={1}
          files={[
            makeFile({ disabled: false }),
            makeFile({ disabled: true, proxy_url: 'http://disabled-only' }),
          ]}
          enabledProxyURLs={['http://used/']}
          enabledCredentialCount={1}
          credentialsError=""
          credentialsLoading={false}
          loading={false}
          saving={false}
          savedURLs={[]}
          error=""
          onClose={() => {}}
          onSave={vi.fn().mockResolvedValue([])}
          onCopy={vi.fn().mockResolvedValue(undefined)}
        />
      );
    });

    const input = renderer!.root.findByProps({ id: 'proxy-filter-input' });
    act(() => {
      input.props.onChange({
        target: { value: 'http://used/\nhttp://free/\nhttp://disabled-only/' },
      });
    });
    act(() => {
      findButton(renderer!, 'accounts.proxy_filter_run').props.onClick();
    });

    const result = renderer!.root.findByProps({
      'aria-label': 'accounts.proxy_filter_result_label',
    });
    expect(result.props.value).toBe('http://free\nhttp://disabled-only');
    renderer!.unmount();
  });

  it('shows the server-saved list for each new modal session', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ProxyFilterModal
          open
          sessionID={8}
          files={[]}
          enabledProxyURLs={[]}
          enabledCredentialCount={0}
          credentialsError=""
          credentialsLoading={false}
          loading={false}
          saving={false}
          savedURLs={['http://saved/']}
          error=""
          onClose={() => {}}
          onSave={vi.fn().mockResolvedValue([])}
          onCopy={vi.fn().mockResolvedValue(undefined)}
        />
      );
    });

    expect(renderer!.root.findByProps({ id: 'proxy-filter-input' }).props.value).toBe(
      'http://saved/'
    );
    act(() => {
      renderer!.root.findByProps({ id: 'proxy-filter-input' }).props.onChange({
        target: { value: 'http://unsaved-edit' },
      });
    });
    act(() => {
      renderer!.update(
        <ProxyFilterModal
          open
          sessionID={9}
          files={[]}
          enabledProxyURLs={[]}
          enabledCredentialCount={0}
          credentialsError=""
          credentialsLoading={false}
          loading={false}
          saving={false}
          savedURLs={['http://saved/']}
          error=""
          onClose={() => {}}
          onSave={vi.fn().mockResolvedValue([])}
          onCopy={vi.fn().mockResolvedValue(undefined)}
        />
      );
    });
    expect(renderer!.root.findByProps({ id: 'proxy-filter-input' }).props.value).toBe(
      'http://saved/'
    );
    renderer!.unmount();
  });
});
