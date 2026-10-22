import { act, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { ProxyReplacementModal } from './ProxyReplacementModal';

const mocks = vi.hoisted(() => ({
  createPreview: vi.fn(),
  confirm: vi.fn(),
  getTask: vi.fn(),
  restoreSuspectProxyPoolStates: vi.fn(),
  copyToClipboard: vi.fn(),
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
    restoreSuspectProxyPoolStates: mocks.restoreSuspectProxyPoolStates,
  },
}));

vi.mock('@/utils/clipboard', () => ({
  copyToClipboard: mocks.copyToClipboard,
}));

const hasText = (value: unknown, text: string): boolean => {
  if (typeof value === 'string') return value.includes(text);
  return Array.isArray(value) && value.some((entry) => hasText(entry, text));
};

const textContent = (node: { children: unknown[] }): string =>
  node.children
    .map((child) =>
      typeof child === 'string' ? child : textContent(child as { children: unknown[] })
    )
    .join('');

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

  it('restores stale pool entries then regenerates an unavailable inspection preview', async () => {
    const unavailableTask = {
      ...draftPage.task,
      source: 'inspection',
      status: 'unavailable',
      canConfirm: false,
      recheckAfterWrite: true,
      totalCount: 46,
      unusedNormalCount: 0,
      fallbackNormalCount: 0,
      suspectExcludedCount: 23,
    };
    mocks.createPreview.mockResolvedValueOnce({ ...draftPage, task: unavailableTask });
    mocks.createPreview.mockResolvedValueOnce(draftPage);
    mocks.restoreSuspectProxyPoolStates.mockResolvedValue({ restoredCount: 23 });
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

    const previewCallsBefore = mocks.createPreview.mock.calls.length;
    await act(async () => {
      findButton(renderer!, 'proxy_replacement.preview').props.onClick();
    });
    await act(async () => {
      findButton(renderer!, 'proxy_replacement.restore_suspects_and_retry').props.onClick();
    });

    expect(mocks.restoreSuspectProxyPoolStates).toHaveBeenCalledWith(
      'https://manager.example',
      'key'
    );
    expect(mocks.createPreview).toHaveBeenCalledTimes(previewCallsBefore + 2);
    renderer!.unmount();
  });

  it('returns every terminal task page to completion reconciliation', async () => {
    const terminalTask = {
      ...draftPage.task,
      source: 'inspection',
      status: 'partial',
      canConfirm: false,
      recheckAfterWrite: true,
      totalCount: 101,
      recheckSuccessCount: 63,
      recheckFailedCount: 38,
    };
    mocks.createPreview.mockResolvedValue({ ...draftPage, task: terminalTask, total: 101 });
    mocks.getTask.mockImplementation(
      async (_base: string, _key: string, _taskID: number, page: number, pageSize: number) => ({
        task: terminalTask,
        items: [
          {
            ...draftPage.items[0],
            id: page,
            target: { ...draftPage.items[0].target, clientKey: `row-${page}` },
            status: page === 1 ? 'recheck_succeeded' : 'recheck_failed',
          },
        ],
        page,
        pageSize,
        total: 101,
      })
    );
    const onCompleted = vi.fn();
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <ProxyReplacementModal
          open
          managerRequestScope={{ apiBase: 'https://manager.example', managementKey: 'key' }}
          origin="inspection"
          targets={draftPage.items.map((item) => item.target)}
          onClose={() => {}}
          onCompleted={onCompleted}
        />
      );
    });

    await act(async () => {
      findButton(renderer!, 'proxy_replacement.preview').props.onClick();
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(mocks.getTask).toHaveBeenCalledWith('https://manager.example', 'key', 71, 1, 100);
    expect(mocks.getTask).toHaveBeenCalledWith('https://manager.example', 'key', 71, 2, 100);
    expect(onCompleted).toHaveBeenCalledWith(
      expect.objectContaining({
        task: terminalTask,
        items: expect.arrayContaining([
          expect.objectContaining({ target: expect.objectContaining({ clientKey: 'row-1' }) }),
          expect.objectContaining({ target: expect.objectContaining({ clientKey: 'row-2' }) }),
        ]),
      })
    );
    renderer!.unmount();
  });

  it('shows and copies the account, original proxy, new proxy, and direct exit-IP recheck outcome', async () => {
    const terminalTask = {
      ...draftPage.task,
      source: 'inspection',
      status: 'partial',
      canConfirm: false,
      recheckAfterWrite: true,
      updatedCount: 1,
      recheckInconclusiveCount: 1,
    };
    const terminalPage = {
      ...draftPage,
      task: terminalTask,
      items: [{ ...draftPage.items[0], status: 'recheck_inconclusive' }],
    };
    mocks.createPreview.mockResolvedValue(terminalPage);
    mocks.getTask.mockResolvedValue(terminalPage);
    mocks.copyToClipboard.mockResolvedValue(true);
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
      await Promise.resolve();
    });

    const row = renderer!.root.findByProps({ 'data-proxy-replacement-mapping-row': true });
    expect(textContent(row)).toContain('http://old:8080');
    expect(textContent(row)).toContain('http://new:8080');
    expect(textContent(row)).toContain('proxy_replacement.item_status_recheck_inconclusive');

    await act(async () => {
      findButton(renderer!, 'proxy_replacement.copy_mapping').props.onClick();
      await Promise.resolve();
    });
    expect(mocks.copyToClipboard).toHaveBeenCalledWith(
      'alice@example.com | http://old:8080 -> http://new:8080 | proxy_replacement.item_status_recheck_inconclusive'
    );
    renderer!.unmount();
  });
});
