import type { AuthFileItem } from '@/types';

/**
 * Returns the canonical representation used by the proxy filter.
 * User-entered whitespace and URL path separators at the end are not
 * significant for this inventory comparison.
 */
export const normalizeProxyFilterURL = (value: unknown): string => {
  if (typeof value !== 'string') return '';
  const trimmed = value.trim();
  if (!trimmed) return '';
  return trimmed.replace(/\/+$/, '');
};

export const normalizeProxyFilterURLs = (values: Iterable<unknown>): string[] => {
  const seen = new Set<string>();
  const normalized: string[] = [];
  for (const value of values) {
    const canonical = normalizeProxyFilterURL(value);
    if (!canonical || seen.has(canonical)) continue;
    seen.add(canonical);
    normalized.push(canonical);
  }
  return normalized;
};

export const readAuthFileProxyURL = (file: Pick<AuthFileItem, 'proxy_url' | 'proxyUrl'>): string =>
  normalizeProxyFilterURL(
    typeof file.proxy_url === 'string' && file.proxy_url.trim() ? file.proxy_url : file.proxyUrl
  );

/**
 * Computes the requested proxy values that are not used by any enabled
 * credential. The caller supplies the complete credential inventory rather
 * than a paginated view.
 */
export const findMissingEnabledProxyURLs = (
  requestedURLs: Iterable<unknown>,
  files: readonly AuthFileItem[]
): string[] => {
  const requested = normalizeProxyFilterURLs(requestedURLs);
  const enabledProxyURLs = new Set(
    files
      .filter((file) => {
        const disabled = file.disabled ?? file['disabled'];
        const runtimeOnly = file['runtime_only'] ?? file.runtimeOnly;
        return (
          disabled !== true &&
          String(disabled ?? '').trim().toLowerCase() !== 'true' &&
          runtimeOnly !== true &&
          String(runtimeOnly ?? '').trim().toLowerCase() !== 'true'
        );
      })
      .map(readAuthFileProxyURL)
      .filter(Boolean)
  );
  return requested.filter((url) => !enabledProxyURLs.has(url));
};
