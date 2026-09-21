import { act, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { AuthJsonPasteModal } from './AuthJsonPasteModal';
import type { AuthJsonInputType } from '@/features/authFiles/sessionAuthConverter';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: (props: { children: ReactNode; footer?: ReactNode }) => (
    <div>
      <div>{props.children}</div>
      <div>{props.footer}</div>
    </div>
  ),
}));

type ModalHarness = {
  renderer: ReactTestRenderer;
  clickSave: () => Promise<void>;
  setJsonText: (value: string) => void;
  setType: (value: AuthJsonInputType) => void;
  getText: () => string;
};

const mountModal = (
  onSave: (type: AuthJsonInputType, fileName: string, jsonText: string) => Promise<void>,
  saving = false,
  disabled = false
): ModalHarness => {
  let renderer: ReactTestRenderer;
  act(() => {
    renderer = create(
      <AuthJsonPasteModal
        open
        saving={saving}
        disabled={disabled}
        onClose={() => {}}
        onSave={onSave}
      />
    );
  });

  const setJsonText = (value: string) => {
    const textarea = renderer!.root.findByProps({ id: 'auth-json-paste-content' });
    act(() => {
      textarea.props.onChange({ target: { value } });
    });
  };

  const setType = (value: AuthJsonInputType) => {
    const select = renderer!.root.findByType(Select);
    act(() => {
      select.props.onChange(value);
    });
  };

  const clickSave = async () => {
    const saveButton = renderer!.root
      .findAllByType(Button)
      .find((node) => node.props.children === 'auth_files.paste_save_button');
    if (!saveButton) throw new Error('Save button not found');
    await act(async () => {
      await saveButton.props.onClick();
    });
  };

  return {
    renderer: renderer!,
    clickSave,
    setJsonText,
    setType,
    getText: () => JSON.stringify(renderer!.toJSON()),
  };
};

describe('AuthJsonPasteModal', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('defaults to CPA authentication JSON', () => {
    const modal = mountModal(vi.fn().mockResolvedValue(undefined));
    const select = modal.renderer.root.findByType(Select);
    expect(select.props.value).toBe('cpa');
    modal.renderer.unmount();
  });

  it('does not render a file-name input and saves with the internal default name', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const modal = mountModal(onSave);
    const json = '{"type":"codex","email":"user@example.com"}';

    modal.setJsonText(json);
    await modal.clickSave();

    expect(modal.renderer.root.findAllByType(Input)).toHaveLength(0);
    expect(modal.getText()).not.toContain('auth_files.paste_file_name_label');
    expect(onSave).toHaveBeenCalledWith('cpa', 'codex-account.json', json);
    modal.renderer.unmount();
  });

  it('rejects empty JSON text without calling save', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const modal = mountModal(onSave);

    modal.setJsonText('   ');
    await modal.clickSave();

    expect(onSave).not.toHaveBeenCalled();
    expect(modal.getText()).toContain('auth_files.paste_error_json_required');
    modal.renderer.unmount();
  });

  it('passes selected CPA type and JSON text to save', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const modal = mountModal(onSave);
    const json = '{"type":"codex","email":"user@example.com"}';

    modal.setType('cpa');
    modal.setJsonText(json);
    await modal.clickSave();

    expect(onSave).toHaveBeenCalledWith('cpa', 'codex-account.json', json);
    modal.renderer.unmount();
  });

  it('passes selected sub2api type with the internal default name to save', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const modal = mountModal(onSave);

    modal.setType('sub2api');
    modal.setJsonText('{"accounts":[]}');

    expect(modal.getText()).toContain('auth_files.paste_sub2api_hint');
    await modal.clickSave();

    expect(onSave).toHaveBeenCalledWith('sub2api', 'codex-account.json', '{"accounts":[]}');
    modal.renderer.unmount();
  });

  it('does not save again while a save is already in progress', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const modal = mountModal(onSave, true);

    modal.setJsonText('{"type":"codex","email":"user@example.com"}');
    await modal.clickSave();

    expect(onSave).not.toHaveBeenCalled();
    modal.renderer.unmount();
  });

  it('does not save while disabled', async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const modal = mountModal(onSave, false, true);

    modal.setJsonText('{"type":"codex","email":"user@example.com"}');
    await modal.clickSave();

    expect(onSave).not.toHaveBeenCalled();
    modal.renderer.unmount();
  });

  it('renders save error returned by onSave', async () => {
    const onSave = vi.fn().mockRejectedValue(new Error('upload failed'));
    const modal = mountModal(onSave);

    modal.setJsonText('{"type":"codex"}');
    await modal.clickSave();

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(modal.getText()).toContain('upload failed');
    modal.renderer.unmount();
  });
});