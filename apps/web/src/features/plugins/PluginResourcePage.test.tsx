import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

type CapturedBridgeOptions = {
  getConfig: () => Promise<unknown>;
  patchConfig: (patch: Record<string, unknown>) => Promise<unknown>;
};

type CapturedBridgeHandler = (() => Promise<void>) & {
  dispose: ReturnType<typeof vi.fn>;
};

const mocks = vi.hoisted(() => ({
  auth: {
    apiBase: 'https://old-cpa.example',
    managementKey: 'old-management-key',
    connectionStatus: 'connected',
    supportsPlugin: true,
  },
  routeParams: { pluginId: 'per-auth-rate-limit', menuIndex: '0' },
  t: (key: string) => key,
  list: vi.fn(),
  getConfig: vi.fn(),
  patchConfig: vi.fn(),
  createBridgeHost: vi.fn(),
  bridgeRecords: [] as Array<{
    options: CapturedBridgeOptions;
    handler: CapturedBridgeHandler;
  }>,
  lifecycleEvents: [] as string[],
}));

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
        path: '/v0/resource/plugins/per-auth-rate-limit/settings',
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
  replyToTrustedPluginFrame: vi.fn(),
}));

vi.mock('./pluginRateLimitBridge', () => ({
  RATE_LIMIT_BRIDGE_PLUGIN_ID: 'per-auth-rate-limit',
  resolveRateLimitBridgeFrameOrigin: () => 'https://old-cpa.example',
  createRateLimitBridgeHost: mocks.createBridgeHost,
}));

import { PluginResourcePage } from './PluginResourcePage';

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
  mocks.list.mockReset().mockResolvedValue({ plugins: [] });
  mocks.getConfig.mockReset().mockResolvedValue({});
  mocks.patchConfig.mockReset().mockResolvedValue(undefined);
  mocks.bridgeRecords.length = 0;
  mocks.lifecycleEvents.length = 0;
  mocks.createBridgeHost.mockReset().mockImplementation((options: CapturedBridgeOptions) => {
    const bridgeIndex = mocks.bridgeRecords.length;
    const handler = Object.assign(
      vi.fn(async () => undefined),
      {
        dispose: vi.fn(() => mocks.lifecycleEvents.push(`dispose:${bridgeIndex}`)),
      }
    ) as unknown as CapturedBridgeHandler;
    mocks.bridgeRecords.push({ options, handler });
    return handler;
  });
});

afterEach(() => {
  act(() => renderer?.unmount());
  vi.unstubAllGlobals();
});

describe('PluginResourcePage rate-limit bridge wiring', () => {
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
});
