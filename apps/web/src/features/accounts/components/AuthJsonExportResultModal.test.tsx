import { act, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { AuthJsonExportResultModal } from './AuthJsonExportResultModal';

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

const textContent = (node: { children: unknown[] }): string =>
  node.children
    .map((child) =>
      typeof child === 'string' ? child : textContent(child as { children: unknown[] })
    )
    .join('');

const findButton = (renderer: ReactTestRenderer, text: string) => {
  const button = renderer.root
    .findAllByType(Button)
    .find((node) => textContent(node as unknown as { children: unknown[] }).includes(text));
  if (!button) throw new Error(`button ${text} not found`);
  return button;
};

describe('AuthJsonExportResultModal', () => {
  it('keeps fallback JSON in the modal only when clipboard copy failed', async () => {
    const retry = vi.fn();
    const download = vi.fn();
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <AuthJsonExportResultModal
          open
          requested={2}
          exported={1}
          failed={[{ clientKey: 'row-b', fileName: 'b.json', code: 'source_unavailable' }]}
          fallbackJson={'[\n  {"email":"a@example.com","refresh_token":"copy-me"}\n]'}
          onClose={vi.fn()}
          onRetryCopy={retry}
          onDownload={download}
        />
      );
    });

    expect(
      renderer!.root.findByProps({ 'data-auth-json-export-fallback': true }).props.value
    ).toContain('copy-me');
    expect(
      textContent(
        renderer!.root.findByProps({
          'data-auth-json-export-failure': 'row-b',
        }) as unknown as { children: unknown[] }
      )
    ).toContain('b.json');
    findButton(renderer!, 'auth_files.export_copy_retry').props.onClick();
    findButton(renderer!, 'auth_files.export_download_json').props.onClick();
    expect(retry).toHaveBeenCalledTimes(1);
    expect(download).toHaveBeenCalledTimes(1);
    renderer!.unmount();
  });

  it('renders only safe failure details when direct clipboard copy succeeded partially', async () => {
    let renderer: ReactTestRenderer;
    await act(async () => {
      renderer = create(
        <AuthJsonExportResultModal
          open
          requested={2}
          exported={1}
          failed={[{ clientKey: 'row-b', fileName: 'b.json', code: 'identity_mismatch' }]}
          onClose={vi.fn()}
        />
      );
    });

    expect(renderer!.root.findAllByProps({ 'data-auth-json-export-fallback': true })).toHaveLength(
      0
    );
    expect(
      textContent(
        renderer!.root.findByProps({
          'data-auth-json-export-failure': 'row-b',
        }) as unknown as { children: unknown[] }
      )
    ).toContain('identity_mismatch');
    expect(JSON.stringify(renderer!.toJSON())).not.toContain('refresh_token');
    renderer!.unmount();
  });
});
