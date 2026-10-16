import { describe, expect, it, vi } from 'vitest';
import { replyToTrustedPluginFrame } from './pluginResourceBridgeReply';

describe('plugin resource bridge reply', () => {
  it('replies to the already-verified event source without re-reading a replaced iframe ref', () => {
    const originalFrame = { postMessage: vi.fn() } as unknown as WindowProxy;
    const replacementFrame = { postMessage: vi.fn() } as unknown as WindowProxy;
    const response = { operation: 'result', ok: true };

    // The request source was verified while the original iframe was active.  A later
    // render may replace the ref, but must not silently discard its in-flight reply.
    expect(replyToTrustedPluginFrame(originalFrame, 'https://cpa.example', response)).toBe(true);
    expect(originalFrame.postMessage).toHaveBeenCalledWith(response, 'https://cpa.example');
    expect(replacementFrame.postMessage).not.toHaveBeenCalled();
  });

  it('does not post when there is no validated frame source or target origin', () => {
    const target = { postMessage: vi.fn() } as unknown as WindowProxy;

    expect(replyToTrustedPluginFrame(null, 'https://cpa.example', { ok: true })).toBe(false);
    expect(replyToTrustedPluginFrame(target, '', { ok: true })).toBe(false);
    expect(target.postMessage).not.toHaveBeenCalled();
  });
});
