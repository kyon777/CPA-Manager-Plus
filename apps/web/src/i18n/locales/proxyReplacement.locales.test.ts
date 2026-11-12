import { describe, expect, it } from 'vitest';
import en from './en.json';
import ru from './ru.json';
import zhCN from './zh-CN.json';
import zhTW from './zh-TW.json';

type ProxyReplacementCopy = {
  inspection_hint: string;
  inspection_recheck: string;
  mapping_account: string;
  mapping_old_proxy: string;
  mapping_new_proxy: string;
  mapping_recheck: string;
  completed_summary: string;
  item_status_recheck_inconclusive: string;
};

const copyFields = (copy: ProxyReplacementCopy): string[] => [
  copy.inspection_hint,
  copy.inspection_recheck,
  copy.mapping_account,
  copy.mapping_old_proxy,
  copy.mapping_new_proxy,
  copy.mapping_recheck,
  copy.completed_summary,
  copy.item_status_recheck_inconclusive,
];

describe('proxy replacement locale copy', () => {
  it('does not expose literal question-mark placeholders in any shipped locale', () => {
    const locales = { en, ru, 'zh-CN': zhCN, 'zh-TW': zhTW };

    for (const [language, resource] of Object.entries(locales)) {
      expect(
        copyFields(resource.proxy_replacement).some((field) => field.includes('?')),
        `${language} proxy replacement copy contains a literal question-mark placeholder`
      ).toBe(false);
    }
  });
});