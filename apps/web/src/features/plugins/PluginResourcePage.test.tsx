import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type CapturedBridgeOptions = {
  expectedOrigin: string;
  isActiveFrameSource: (source: MessageEventSource | null) => boolean;
  getConfig: () => Promise<Record<string, unknown>>;
  patchConfig: (patch: Record<string, unknown>) => Promise<unknown>;
  reply: (source: MessageEventSource | null, response: unknown) => void;
};

type CapturedBridgeHandler = ((event: {
  data: unknown;
  origin: string;
  source: MessageEventSource | null;
}) => Promise<void>) & {
  dispose: () => void;
};

const mocks = vi.hoisted(() => ({
  auth: {
    apiBase: 'https://old-cpa.example',
    managementKey: 'old-management-key',
    connectionStatus: 'connected',
    supportsPlugin: true,
  },
  routeParams: { pluginId: 'per-auth-rate-limit', menuIndex: '0' },
  resourcePath: '/v0/resource/plugins/per-auth-rate-limit/settings',
  t: (key: string) => key,
  list: vi.fn(),
  getConfig: vi.fn(),
  patchConfig: vi.fn(),
  bridgeRecords: [] as Array<{
    options: CapturedBridgeOptions;
    handler: CapturedBridgeHandler;
  }>,
  lifecycleEvents: [] as string[],
  replyToTrustedPluginFrame: vi.fn(),
}));

const createDeferred = <T,>() => {
  let resolve!: (value: T | PromiseLike<T>) => void;
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise;
  });
  return { promise, resolve };
};

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: mocks.t }),
}));

vi.mock('react-router-dom', () => ({
  useParams: () => mocks.routeParams,
}));

vi.mock('@/components/ui/EmptyState', () => ({
  EmptyState: () => null,
}));

vi.mock('@/hooks/useHeaderRefresh', () => ({
  useHeaderRefresh: () => undefined,
}));

vi.mock('@/services/api', () => ({
  pluginsApi: {
    list: mocks.list,
    getConfig: mocks.getConfig,
    patchConfig: mocks.patchConfig,
  },
  apiKeyModelPolicyApi: {
    getPolicy: vi.fn(),
    putPolicy: vi.fn(),
  },
  apiKeysApi: {
    list: vi.fn(),
    replace: vi.fn(),
  },
  usageServiceApi: {
    getApiKeyAliases: vi.fn(),
  },
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof mocks.auth) => unknown) => selector(mocks.auth),
  useThemeStore: (selector: (state: { resolvedTheme: string }) => unknown) =>
    selector({ resolvedTheme: 'light' }),
}));

vi.mock('./pluginHostStyle', () => ({
  createPluginHostStyleBridge: () => null,
}));

vi.mock('./pluginResources', () => ({
  PLUGIN_RESOURCES_REFRESH_EVENT: 'plugin-resources-refresh',
  collectPluginResourceEntries: () => [
    {
      pluginID: 'per-auth-rate-limit',
      pluginTitle: 'Rate Limit',
      pluginLogo: '',
      menuIndex: 0,
      menu: {
        menu: 'Settings',
        path: mocks.resourcePath,
        description: '',
      },
      label: 'Settings',
      description: 'Rate limit settings',
      route: '/plugin-pages/per-auth-rate-limit/0',
    },
  ],
  resolvePluginAssetURL: (value: string, apiBase: string) => `${apiBase}${value}`,
}));

vi.mock('./pluginResourceBridgeRouting', () => ({
  appendPluginResourceBridgeParentOrigin: (source: string) => source,
}));

vi.mock('./pluginResourceBridgeReply', () => ({
  replyToTrustedPluginFrame: mocks.replyToTrustedPluginFrame,
}));

vi.mock('./pluginRateLimitBridge', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./pluginRateLimitBridge')>();
  return {
    ...actual,
    createRateLimitBridgeHost: (options: CapturedBridgeOptions) => {
      const actualHandler = actual.createRateLimitBridgeHost(options);
      const originalDispose = actualHandler.dispose;
      const bridgeIndex = mocks.bridgeRecords.length;
      const dispose = vi.fn(() => {
        mocks.lifecycleEvents.push(`dispose:${bridgeIndex}`);
        originalDispose();
      });
      const handler = Object.assign(actualHandler, { dispose }) as unknown as CapturedBridgeHandler;
      mocks.bridgeRecords.push({ options, handler });
      return handler;
    },
  };
});

import { PluginResourcePage } from './PluginResourcePage';
import { RATE_LIMIT_BRIDGE_PROTOCOL } from './pluginRateLimitBridge';

let renderer: ReactTestRenderer;
let windowListeners: Map<string, unknown>;
let windowStub: {
  location: { origin: string };
  addEventListener: ReturnType<typeof vi.fn>;
  removeEventListener: ReturnType<typeof vi.fn>;
  dispatchEvent: ReturnType<typeof vi.fn>;
};

beforeEach(() => {
  windowListeners = new Map();
  windowStub = {
    location: { origin: 'https://manager.example' },
    addEventListener: vi.fn((type: string, listener: unknown) => {
      windowListeners.set(type, listener);
    }),
    removeEventListener: vi.fn((type: string, listener: unknown) => {
      if (windowListeners.get(type) === listener) windowListeners.delete(type);
      mocks.lifecycleEvents.push(`remove:${type}`);
    }),
    dispatchEvent: vi.fn(),
  };
  vi.stubGlobal('window', windowStub);
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  mocks.auth.apiBase = 'https://old-cpa.example';
  mocks.auth.managementKey = 'old-management-key';
  mocks.auth.connectionStatus = 'connected';
  mocks.auth.supportsPlugin = true;
  mocks.resourcePath = '/v0/resource/plugins/per-auth-rate-limit/settings';
  mocks.list.mockReset().mockResolvedValue({ plugins: [] });
  mocks.getConfig.mockReset().mockResolvedValue({});
  mocks.patchConfig.mockReset().mockResolvedValue(undefined);
  mocks.replyToTrustedPluginFrame.mockReset();
  mocks.bridgeRecords.length = 0;
  mocks.lifecycleEvents.length = 0;
});

afterEach(() => {
  act(() => renderer?.unmount());
  vi.unstubAllGlobals();
});

describe('PluginResourcePage rate-limit bridge wiring', () => {
  const legacySettings = {
    enabled: true,
    rpm: 1,
    minInterval: '1s',
    maxQueueWait: '35s',
    maxPendingRequests: 500,
  };
  const modernSettings = {
    ...legacySettings,
    concurrencyEnabled: true,
    maxConcurrentPerAuth: 1,
    affinityWait: '15s',
    allCandidatesWait: '30s',
    retryInitialInterval: '250ms',
    retryMaxInterval: '1s',
    rebindSessionAfterAffinityWait: true,
  };

  const createSaveEvent = (
    requestID: string,
    settings: Record<string, unknown>,
    source: MessageEventSource
  ) => ({
    data: {
      protocol: RATE_LIMIT_BRIDGE_PROTOCOL,
      pluginID: 'per-auth-rate-limit',
      requestID,
      operation: 'save',
      settings,
    },
    origin: 'https://old-cpa.example',
    source,
  });

  it('disposes the old bridge before listener removal and keeps its immutable scope', async () => {
    const frameWindow = {};
    await act(async () => {
      renderer = create(<PluginResourcePage />, {
        createNodeMock: (element) =>
          element.type === 'iframe' ? { contentWindow: frameWindow } : {},
      });
      await Promise.resolve();
    });

    expect(mocks.bridgeRecords).toHaveLength(1);
    const oldBridge = mocks.bridgeRecords[0];
    if (!oldBridge) throw new Error('expected initial rate-limit bridge');

    mocks.auth.apiBase = 'https://new-cpa.example';
    mocks.auth.managementKey = 'new-management-key';
    await act(async () => {
      renderer.update(<PluginResourcePage />);
      await Promise.resolve();
    });

    expect(oldBridge.handler.dispose).toHaveBeenCalledTimes(1);
    expect(mocks.lifecycleEvents.indexOf('dispose:0')).toBeGreaterThanOrEqual(0);
    expect(mocks.lifecycleEvents.indexOf('remove:message')).toBeGreaterThan(
      mocks.lifecycleEvents.indexOf('dispose:0')
    );
    expect(mocks.bridgeRecords).toHaveLength(2);

    await oldBridge.options.getConfig();
    await oldBridge.options.patchConfig({ enabled: false });

    expect(mocks.getConfig).toHaveBeenLastCalledWith(
      'per-auth-rate-limit',
      expect.objectContaining({
        apiBase: 'https://old-cpa.example',
        managementKey: 'old-management-key',
      })
    );
    expect(mocks.patchConfig).toHaveBeenLastCalledWith(
      'per-auth-rate-limit',
      { enabled: false },
      expect.objectContaining({
        apiBase: 'https://old-cpa.example',
        managementKey: 'old-management-key',
      })
    );
  });

  it('disposes on connection loss with the same scope before queued saves can call APIs or reply', async () => {
    const frameWindow = {};
    const firstPatchStarted = createDeferred<void>();
    const releaseFirstPatch = createDeferred<void>();
    const trustedSource = frameWindow as MessageEventSource;
    await act(async () => {
      renderer = create(<PluginResourcePage />, {
        createNodeMock: (element) =>
          element.type === 'iframe' ? { contentWindow: frameWindow } : {},
      });
      await Promise.resolve();
    });

    const oldBridge = mocks.bridgeRecords[0];
    if (!oldBridge) throw new Error('expected initial rate-limit bridge');
    mocks.patchConfig.mockImplementation(async () => {
      firstPatchStarted.resolve();
      await releaseFirstPatch.promise;
    });

    const firstSave = oldBridge.handler(
      createSaveEvent('connection_first', legacySettings, trustedSource)
    );
    const queuedSave = oldBridge.handler(
      createSaveEvent('connection_queued', modernSettings, trustedSource)
    );
    await firstPatchStarted.promise;

    mocks.auth.connectionStatus = 'disconnected';
    await act(async () => {
      renderer.update(<PluginResourcePage />);
      await Promise.resolve();
    });

    expect(oldBridge.handler.dispose).toHaveBeenCalledTimes(1);
    releaseFirstPatch.resolve();
    await Promise.all([firstSave, queuedSave]);

    expect(mocks.getConfig).not.toHaveBeenCalled();
    expect(mocks.patchConfig).toHaveBeenCalledTimes(1);
    expect(mocks.replyToTrustedPluginFrame).not.toHaveBeenCalled();
  });

  it('disposes when the connected backend stops supporting plugins', async () => {
    const frameWindow = {};
    await act(async () => {
      renderer = create(<PluginResourcePage />, {
        createNodeMock: (element) =>
          element.type === 'iframe' ? { contentWindow: frameWindow } : {},
      });
      await Promise.resolve();
    });

    const oldBridge = mocks.bridgeRecords[0];
    if (!oldBridge) throw new Error('expected initial rate-limit bridge');
    mocks.auth.supportsPlugin = false;
    await act(async () => {
      renderer.update(<PluginResourcePage />);
      await Promise.resolve();
    });

    expect(oldBridge.handler.dispose).toHaveBeenCalledTimes(1);
    expect(mocks.bridgeRecords).toHaveLength(1);
  });

  it('disposes the old bridge when a same-origin resource URL generation changes', async () => {
    const frameWindow = {};
    await act(async () => {
      renderer = create(<PluginResourcePage />, {
        createNodeMock: (element) =>
          element.type === 'iframe' ? { contentWindow: frameWindow } : {},
      });
      await Promise.resolve();
    });

    const oldBridge = mocks.bridgeRecords[0];
    if (!oldBridge) throw new Error('expected initial rate-limit bridge');
    mocks.resourcePath = '/v0/resource/plugins/per-auth-rate-limit/settings?generation=2';
    mocks.list.mockImplementation(async () => ({ plugins: [] }));
    const refreshListener = windowListeners.get('plugin-resources-refresh');
    if (typeof refreshListener !== 'function') throw new Error('expected refresh listener');

    await act(async () => {
      await refreshListener(new Event('plugin-resources-refresh'));
    });

    expect(oldBridge.handler.dispose).toHaveBeenCalledTimes(1);
    expect(mocks.bridgeRecords).toHaveLength(2);
  });
});
