import { describe, expect, it } from 'vitest';
import {
  API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
  API_KEY_MODEL_POLICY_BRIDGE_PARENT_ORIGIN_PARAM,
} from './pluginApiKeyModelPolicyBridge';
import { RATE_LIMIT_BRIDGE_PLUGIN_ID } from './pluginRateLimitBridge';
import {
  appendPluginResourceBridgeParentOrigin,
  resolvePluginResourceBridgeKind,
} from './pluginResourceBridgeRouting';

describe('plugin resource bridge routing', () => {
  it('selects only the two explicitly supported bridge plugins', () => {
    expect(
      resolvePluginResourceBridgeKind(RATE_LIMIT_BRIDGE_PLUGIN_ID, 'https://cpa.example')
    ).toBe('rate_limit');
    expect(
      resolvePluginResourceBridgeKind(API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID, 'https://cpa.example')
    ).toBe('api_key_model_policy');
    expect(resolvePluginResourceBridgeKind('unknown-plugin', 'https://cpa.example')).toBe('none');
    expect(resolvePluginResourceBridgeKind(API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID, '')).toBe('none');
  });

  it('carries only the parent origin and preserves the resource origin/query', () => {
    const source = appendPluginResourceBridgeParentOrigin(
      API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
      'https://cpa.example/v0/resource/plugins/api-key-model-policy/settings?theme=dark',
      'https://cpam.example'
    );
    const url = new URL(source);

    expect(url.origin).toBe('https://cpa.example');
    expect(url.searchParams.get('theme')).toBe('dark');
    expect(url.searchParams.get(API_KEY_MODEL_POLICY_BRIDGE_PARENT_ORIGIN_PARAM)).toBe(
      'https://cpam.example'
    );
  });

  it('does not add a bridge query for an unrelated plugin or invalid origin', () => {
    const source = 'https://cpa.example/resource?theme=dark';
    expect(
      appendPluginResourceBridgeParentOrigin('unknown-plugin', source, 'https://cpam.example')
    ).toBe(source);
    expect(
      appendPluginResourceBridgeParentOrigin(
        API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
        source,
        'javascript:alert(1)'
      )
    ).toBe(source);
  });
});
