import { act, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { ProxyReplacementModal } from './ProxyReplacementModal';

const mocks = vi.hoisted(() => ({
  createPreview: vi.fn(),
  confirm: vi.fn(),
  getTask: vi.fn(),
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
    createProxyReplacementPreview: mocks.createPreview,
    confirmProxyReplacementTask: mocks.confirm,
    getProxyReplacementTask: mocks.getTask,
  },
}));

const hasText = (value: unknown, text: string): boolean => {
  if (typeof value === 'string') return value.includes(text);
  return Array.isArray(value) && value.some((entry) => hasText(entry, text));
};

const findButton = (renderer: ReactTestRenderer, text: string) => {
  const button = renderer.root
    .findAllByType(Button)
    .find((node) => hasText(node.props.children, text));
  if (!button) throw new Error(`button ${text} not found`);
  return button;
};

const draftPage = {
  task: {
    id: 71,
    source: 'batch_pool',
    mode: 'pool',
    status: 'draft',
    recheckAfterWrite: false,
    canConfirm: true,
    taskOffset: 0,
    totalCount: 1,
    updatedCount: 0,
    failedCount: 0,
    skippedCount: 0,
    recheckSuccessCount: 0,
    recheckFailedCount: 0,
    unusedNormalCount: 1,
    fallbackNormalCount: 0,
    suspectExcludedCount: 0,
    createdAtMs: 1,
    updatedAtMs: 1,
  },
  items: [
    {
      id: 1,
      taskId: 71,
      itemOrder: 0,
      target: {
        clientKey: 'row-1',
        fileName: 'alice.json',
        provider: 'codex',
        accountEmail: 'alice@example.com',
      },
      oldProxyUrl: 'http://old:8080',
      newProxyUrl: 'http://new:8080',
      status: 'planned',
    },
  ],
  page: 1,
  pageSize: 1,
  total: 1,
};

describe('ProxyReplacementModal', () => {
  it('creates a pool preview for manual selected credentials before confirmation', async () => {
    mocks.createPreview.mockResolvedValue(draftPage);
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProxyReplacementModal
          open
          managerRequestScope={{ apiBase: 'https://manager.example', managementKey: 'key' }}
          origin="accounts"
          targets={draftPage.items.map((item) => item.target)}
          onClose={() => {}}
        />
      );
    });

    await act(async () => {
      findButton(renderer!, 'proxy_replacement.preview').props.onClick();
    });

    expect(mocks.createPreview).toHaveBeenCalledWith(
      'https://manager.example',
      'key',
      expect.objectContaining({ source: 'batch_pool', mode: 'pool', targets: draftPage.items.map((item) => item.target) })
    );
    expect(renderer!.root.findByProps({ 'data-proxy-replacement-preview': true }).props.children).toBeTruthy();
    renderer!.unmount();
  });

  it('forces inspection replacements to pool allocation and identifies recheck behavior', async () => {
    mocks.createPreview.mockResolvedValue({
      ...draftPage,
      task: { ...draftPage.task, source: 'inspection', recheckAfterWrite: true },
    });
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProxyReplacementModal
          open
          managerRequestScope={{ apiBase: 'https://manager.example', managementKey: 'key' }}
          origin="inspection"
          targets={draftPage.items.map((item) => item.target)}
          onClose={() => {}}
        />
      );
    });

    await act(async () => {
      findButton(renderer!, 'proxy_replacement.preview').props.onClick();
    });

    expect(mocks.createPreview).toHaveBeenCalledWith(
      'https://manager.example',
      'key',
      expect.objectContaining({ source: 'inspection', mode: 'pool' })
    );
    expect(renderer!.root.findByProps({ 'data-proxy-replacement-recheck': true }).props.children).toBeTruthy();
    renderer!.unmount();
  });
});
