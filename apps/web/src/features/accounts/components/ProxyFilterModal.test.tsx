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
  verifyProxyPoolExitIPs: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, unknown>) =>
      typeof values?.ip === 'string' ? `${key}:${values.ip}` : key,
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

vi.mock('@/services/api/usageService', () => ({
  usageServiceApi: {
    listProxyPoolStates: mocks.listProxyPoolStates,
    restoreProxyPoolState: mocks.restoreProxyPoolState,
    verifyProxyPoolExitIPs: mocks.verifyProxyPoolExitIPs,
  },
}));

const makeFile = (overrides: Partial<AuthFileItem>): AuthFileItem => ({
  name: 'credential.json',
  ...overrides,
});

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

describe('ProxyFilterModal', () => {
  it('keeps timeout evidence pending and verifies one proxy through its fingerprint only', async () => {
    const proxyURL = 'http://user:secret@pending.example:8080/';
    const fingerprint = proxyReplacementFingerprint(proxyURL);
    mocks.listProxyPoolStates
      .mockResolvedValueOnce({
        items: [
          {
            fingerprint,
            status: 'pending_verification',
            lastFailureReason: 'transport_timeout',
            updatedAtMs: 1,
          },
        ],
      })
      .mockResolvedValueOnce({ items: [] });
    mocks.verifyProxyPoolExitIPs.mockResolvedValue({
      results: [{ fingerprint, outcome: 'healthy', exitIp: '203.0.113.7', latencyMs: 31 }],
    });
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProxyFilterModal
          open
          sessionID={90}
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

    expect(renderer!.root.findByProps({ 'data-proxy-pool-pending': fingerprint })).toBeTruthy();
    await act(async () => {
      findButton(renderer!, 'accounts.proxy_pool_verify_one').props.onClick();
    });
    expect(mocks.verifyProxyPoolExitIPs).toHaveBeenCalledWith('https://manager.example', 'key', [
      fingerprint,
    ]);
    expect(
      renderer!.root.findAll((node) => hasText(node.props.children, '203.0.113.7')).length
    ).toBeGreaterThan(0);
    renderer!.unmount();
  });

  it('disables a second batch self-check while the first is running', async () => {
    const firstProxy = 'http://pending-one.example:8080';
    const secondProxy = 'http://suspect-two.example:8080';
    mocks.listProxyPoolStates.mockResolvedValue({
      items: [
        {
          fingerprint: proxyReplacementFingerprint(firstProxy),
          status: 'pending_verification',
          lastFailureReason: 'transport_timeout',
          updatedAtMs: 1,
        },
        {
          fingerprint: proxyReplacementFingerprint(secondProxy),
          status: 'suspect',
          lastFailureReason: 'proxy_connect_timeout',
          updatedAtMs: 2,
        },
      ],
    });
    let release!: (value: { results: [] }) => void;
    mocks.verifyProxyPoolExitIPs.mockReturnValue(
      new Promise((resolve) => {
        release = resolve;
      })
    );
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProxyFilterModal
          open
          sessionID={92}
          files={[]}
          enabledProxyURLs={[]}
          enabledCredentialCount={0}
          credentialsError=""
          credentialsLoading={false}
          loading={false}
          saving={false}
          savedURLs={[firstProxy, secondProxy]}
          error=""
          managerRequestScope={{ apiBase: 'https://manager.example', managementKey: 'key' }}
          onClose={() => {}}
          onSave={vi.fn().mockResolvedValue([])}
          onCopy={vi.fn().mockResolvedValue(undefined)}
        />
      );
    });
    act(() => {
      findButton(renderer!, 'accounts.proxy_pool_verify_all').props.onClick();
    });
    expect(findButton(renderer!, 'accounts.proxy_pool_verify_all').props.disabled).toBe(true);
    await act(async () => {
      release({ results: [] });
    });
    renderer!.unmount();
  });

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
      renderer!.root.findByProps({
        'data-proxy-pool-suspect': proxyReplacementFingerprint(proxyURL),
      })
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
