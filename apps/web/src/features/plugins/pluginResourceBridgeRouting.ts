import {
  appendApiKeyModelPolicyBridgeParentOrigin,
  API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID,
  resolveApiKeyModelPolicyBridgeFrameOrigin,
} from './pluginApiKeyModelPolicyBridge';
import {
  appendRateLimitBridgeParentOrigin,
  RATE_LIMIT_BRIDGE_PLUGIN_ID,
  resolveRateLimitBridgeFrameOrigin,
} from './pluginRateLimitBridge';

export type PluginResourceBridgeKind = 'rate_limit' | 'api_key_model_policy' | 'none';

export const resolvePluginResourceBridgeKind = (
  pluginID: string,
  frameOrigin: string
): PluginResourceBridgeKind => {
  if (!frameOrigin) return 'none';
  if (pluginID === RATE_LIMIT_BRIDGE_PLUGIN_ID) return 'rate_limit';
  if (pluginID === API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID) return 'api_key_model_policy';
  return 'none';
};

export const appendPluginResourceBridgeParentOrigin = (
  pluginID: string,
  source: string,
  parentOrigin: string
): string => {
  if (pluginID === RATE_LIMIT_BRIDGE_PLUGIN_ID) {
    const frameOrigin = resolveRateLimitBridgeFrameOrigin(source, parentOrigin);
    return frameOrigin ? appendRateLimitBridgeParentOrigin(source, parentOrigin) : source;
  }
  if (pluginID === API_KEY_MODEL_POLICY_BRIDGE_PLUGIN_ID) {
    const frameOrigin = resolveApiKeyModelPolicyBridgeFrameOrigin(source, parentOrigin);
    return frameOrigin ? appendApiKeyModelPolicyBridgeParentOrigin(source, parentOrigin) : source;
  }
  return source;
};
