# å®¢æˆ·ç«¯ Key é¢„ä¼°æˆæœ¬å€çŽ‡æ˜¾ç¤º Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** åœ¨ Usage Analytics çš„â€œå®¢æˆ·ç«¯ Key æ¶ˆè€—æŽ’è¡Œâ€è¡¨æ ¼ä¸­å¢žåŠ ä¸€ä¸ªçº¯å‰ç«¯å€çŽ‡è¾“å…¥ï¼Œå¹¶åœ¨çŽ°æœ‰é¢„ä¼°æˆæœ¬é‡‘é¢åŽä»¥å†…è”æ‹¬å·æ˜¾ç¤ºå€çŽ‡é‡‘é¢ã€‚

**Architecture:** åœ¨ `UsageAnalyticsPageInner` ä¿å­˜è¾“å…¥æ¡†åŽŸå§‹å­—ç¬¦ä¸²ï¼Œä½¿ç”¨ `usageAnalyticsModel.ts` ä¸­çš„çº¯å‡½æ•°å°†ç©ºå€¼ã€éžæ³•å€¼å’Œè´Ÿæ•°å½’ä¸€åŒ–ä¸º `1`ã€‚ä»…æŠŠå½’ä¸€åŒ–åŽçš„å±•ç¤ºå€çŽ‡ä¼ å…¥ `RankTable` çš„ `apiKey` åˆ†æ”¯ï¼›`RankTable` ä¿ç•™åŽŸå§‹ `row.estimatedCost`ï¼Œåªåœ¨æ¸²æŸ“åŒä¸€ä¸ªæˆæœ¬å•å…ƒæ ¼æ—¶è¿½åŠ å€çŽ‡é‡‘é¢ã€‚æ‰€æœ‰æ•°æ®è¯·æ±‚ã€æŽ’åºã€æ‘˜è¦å’Œè®¡è´¹å­—æ®µä¿æŒä¸å˜ã€‚

**Tech Stack:** React 19ã€TypeScriptã€React Test Rendererã€Vitestã€CSS Modulesã€i18nextã€‚

---

## æ–‡ä»¶æ”¹åŠ¨æ¸…å•

- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.ts` â€” å¢žåŠ å¯å•æµ‹çš„å€çŽ‡å½’ä¸€åŒ–çº¯å‡½æ•°ï¼›ä¸å¢žåŠ æœåŠ¡ç«¯æ¨¡åž‹å­—æ®µã€‚
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.test.ts` â€” è¦†ç›–é»˜è®¤å€¼ã€åˆæ³•å°æ•°ã€é›¶å€¼å’Œéžæ³•è¾“å…¥ã€‚
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.tsx` â€” å¢žåŠ å½“å‰é¡µé¢çŠ¶æ€ã€å€çŽ‡æŽ§ä»¶ã€`RankTable` å±•ç¤ºå‚æ•°å’Œå†…è”é‡‘é¢æ¸²æŸ“ã€‚
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx` â€” è¦†ç›–é»˜è®¤æ˜¾ç¤ºã€å³æ—¶é‡ç®—ã€å¤±ç„¦å½’ä¸€åŒ–ã€ä»… apiKeys è¡¨æ ¼ç”Ÿæ•ˆå’Œä¸è§¦å‘è¯·æ±‚ã€‚
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.module.scss` â€” å¢žåŠ å€çŽ‡æŽ§ä»¶ä¸Žæ‹¬å·é‡‘é¢çš„ç´§å‡‘å•è¡Œå¸ƒå±€ï¼Œä¸æ”¹å˜å…¶ä»–è¡¨æ ¼æ ·å¼ã€‚
- Modify: `apps/web/src/i18n/locales/zh-CN.json` â€” å¢žåŠ å€çŽ‡è®¾ç½®å’Œå€çŽ‡é‡‘é¢æ–‡æ¡ˆã€‚
- Modify: `apps/web/src/i18n/locales/zh-TW.json` â€” å¢žåŠ ç¹ä½“ä¸­æ–‡æ–‡æ¡ˆã€‚
- Modify: `apps/web/src/i18n/locales/en.json` â€” å¢žåŠ è‹±æ–‡æ–‡æ¡ˆã€‚
- Modify: `apps/web/src/i18n/locales/ru.json` â€” å¢žåŠ ä¿„æ–‡æ–‡æ¡ˆã€‚

æ‰€æœ‰å‘½ä»¤å‡ä»Ž `C:\kyon\docker\cpa-manager\source` æ‰§è¡Œã€‚å½“å‰ `apps/manager-server/**` çš„æ—¢æœ‰ WIP ä¸å¾—åŠ å…¥æœ¬åŠŸèƒ½æäº¤ã€‚

### Task 1: ä¸ºå€çŽ‡è¾“å…¥å®šä¹‰å½’ä¸€åŒ–å¥‘çº¦ï¼ˆREDï¼‰

**Files:**
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.test.ts`

- [x] **Step 1: æ·»åŠ å¤±è´¥æµ‹è¯•**

åœ¨çŽ°æœ‰ model æµ‹è¯• import ä¸­åŠ å…¥ `normalizeUsageDisplayMultiplier`ï¼Œå¹¶åœ¨æµ‹è¯•æ–‡ä»¶ä¸­åŠ å…¥ï¼š

```ts
describe('usage display multiplier', () => {
  it.each([
    ['1', 1],
    ['0.3', 0.3],
    ['0', 0],
    ['', 1],
    ['   ', 1],
    ['-0.1', 1],
    ['NaN', 1],
    ['Infinity', 1],
  ])('normalizes %s to %s', (input, expected) => {
    expect(normalizeUsageDisplayMultiplier(input)).toBe(expected);
  });
});
```

- [x] **Step 2: è¿è¡Œæµ‹è¯•ç¡®è®¤æ˜¯é¢„æœŸå¤±è´¥**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/usageAnalyticsModel.test.ts --testNamePattern="usage display multiplier"
```

Expected: FAIL because `normalizeUsageDisplayMultiplier` has not been exported yetï¼›ä¸å¾—é€šè¿‡ä¿®æ”¹æµ‹è¯•æ¥ç»•è¿‡ç¼ºå¤±å¯¼å‡ºã€‚

### Task 2: å®žçŽ°å¹¶éªŒè¯å€çŽ‡å½’ä¸€åŒ–çº¯å‡½æ•°ï¼ˆGREENï¼‰

**Files:**
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.ts`
- Test: `apps/web/src/features/usage-analytics/usageAnalyticsModel.test.ts`

- [x] **Step 1: æ·»åŠ æœ€å°å®žçŽ°**

åœ¨çŽ°æœ‰ `formatMetricValue` é™„è¿‘å¯¼å‡ºä»¥ä¸‹å‡½æ•°ï¼š

```ts
export const normalizeUsageDisplayMultiplier = (value: string): number => {
  const trimmed = value.trim();
  if (!trimmed) return 1;

  const parsed = Number(trimmed);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 1;
};
```

è¯¥å‡½æ•°åªå¤„ç†å±•ç¤ºè¾“å…¥ï¼Œä¸å†™å…¥ä»»ä½• Usage API å‚æ•°æˆ–åŽç«¯æ¨¡åž‹ã€‚

- [x] **Step 2: è¿è¡ŒåŒä¸€æµ‹è¯•ç¡®è®¤é€šè¿‡**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/usageAnalyticsModel.test.ts --testNamePattern="usage display multiplier"
```

Expected: PASSï¼Œ8 ä¸ªç”¨ä¾‹å…¨éƒ¨é€šè¿‡ã€‚

### Task 3: å…ˆå†™å®¢æˆ·ç«¯ Key è¡¨æ ¼è¡Œä¸ºæµ‹è¯•ï¼ˆREDï¼‰

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx`

- [x] **Step 1: æ·»åŠ æµ‹è¯•è¾…åŠ©å‡½æ•°**

åœ¨çŽ°æœ‰ `getText`/`renderPage` è¾…åŠ©å‡½æ•°é™„è¿‘æ·»åŠ æŒ‰ aria-label æŸ¥æ‰¾å€çŽ‡è¾“å…¥å’Œè¯»å–æˆæœ¬åˆ—çš„è¾…åŠ©é€»è¾‘ï¼š

```ts
const findApiKeyMultiplierInput = (renderer: ReactTestRenderer) => {
  const input = renderer.root.findAllByType('input').find(
    (node) => node.props['aria-label'] === 'usage_analytics.api_key_multiplier_setting'
  );
  if (!input) throw new Error('API key multiplier input not found');
  return input;
};

const getApiKeyCostCellText = (renderer: ReactTestRenderer) => {
  const section = renderer.root.findAllByType('section').find((candidate) =>
    candidate
      .findAllByType('h2')
      .some((heading) => getText(heading) === 'usage_analytics.api_key_rank_title')
  );
  if (!section) throw new Error('API key rank section not found');
  const row = section.findAllByType('tbody')[0]?.findAllByType('tr')[0];
  if (!row) throw new Error('API key rank row not found');
  return getText(row.findAllByType('td')[8]!);
};
```

- [x] **Step 2: æ·»åŠ å¤±è´¥æµ‹è¯•ï¼Œé”å®šç”¨æˆ·å¯è§è¡Œä¸º**

åœ¨ `UsageAnalyticsPage.test.tsx` çš„ apiKeys æµ‹è¯•é™„è¿‘åŠ å…¥ï¼š

```tsx
it('shows the original and inline multiplier cost only in the API key rank table', () => {
  const usageState = createUsageState({ activeTab: 'apiKeys' });
  mocks.usageState = usageState;
  const renderer = renderPage();

  expect(findApiKeyMultiplierInput(renderer).props.value).toBe('1');
  expect(getApiKeyCostCellText(renderer)).toContain('$1.25');
  expect(getApiKeyCostCellText(renderer)).toContain('usage_analytics.api_key_multiplier_amount');
  expect(usageState.refresh).not.toHaveBeenCalled();

  act(() => {
    findApiKeyMultiplierInput(renderer).props.onChange({ target: { value: '0.3' } });
  });

  expect(getApiKeyCostCellText(renderer)).toContain('$0.38');
  expect(usageState.refresh).not.toHaveBeenCalled();
});

it('normalizes an empty multiplier on blur without changing the API key data row', () => {
  const usageState = createUsageState({ activeTab: 'apiKeys' });
  mocks.usageState = usageState;
  const renderer = renderPage();
  const input = findApiKeyMultiplierInput(renderer);

  act(() => {
    input.props.onChange({ target: { value: '' } });
  });
  expect(getApiKeyCostCellText(renderer)).toContain('$1.25');

  act(() => {
    findApiKeyMultiplierInput(renderer).props.onBlur();
  });
  expect(findApiKeyMultiplierInput(renderer).props.value).toBe('1');
  expect(usageState.apiKeyRows[0]?.estimatedCost).toBe(1.25);
});

it('does not render the multiplier control outside the API key tab', () => {
  mocks.usageState = createUsageState({ activeTab: 'models' });
  const renderer = renderPage();
  expect(
    renderer.root.findAllByProps({ 'aria-label': 'usage_analytics.api_key_multiplier_setting' })
  ).toHaveLength(0);
});
```

- [x] **Step 3: è¿è¡Œæµ‹è¯•ç¡®è®¤æ˜¯é¢„æœŸå¤±è´¥**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/UsageAnalyticsPage.test.tsx --testNamePattern="multiplier"
```

Expected: FAIL because the input, multiplier prop and inline rendering do not exist yetï¼›å¤±è´¥åŽŸå› åº”æ˜¯æ‰¾ä¸åˆ°å€çŽ‡è¾“å…¥ï¼Œè€Œä¸æ˜¯æµ‹è¯•çŽ¯å¢ƒå¯åŠ¨é”™è¯¯ã€‚

### Task 4: æŽ¥å…¥é¡µé¢çŠ¶æ€ä¸Ž RankTable å±•ç¤ºï¼ˆGREENï¼‰

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.tsx`
- Test: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx`

- [x] **Step 1: å¯¼å…¥å½’ä¸€åŒ–å‡½æ•°å¹¶å¢žåŠ å±€éƒ¨é¡µé¢çŠ¶æ€**

åœ¨çŽ°æœ‰ model import ä¸­åŠ å…¥ `normalizeUsageDisplayMultiplier`ï¼Œåœ¨ `UsageAnalyticsPageInner` çš„å…¶ä»– `useState` é™„è¿‘åŠ å…¥ï¼š

```tsx
const [apiKeyCostMultiplierInput, setApiKeyCostMultiplierInput] = useState('1');
const apiKeyCostMultiplier = normalizeUsageDisplayMultiplier(apiKeyCostMultiplierInput);
```

è¾“å…¥çŠ¶æ€åªå­˜åœ¨å½“å‰é¡µé¢å®žä¾‹ä¸­ï¼›ä¸å†™ localStorageã€ä¸å†™ URLã€ä¸å‘è¯·æ±‚ã€‚

- [x] **Step 2: åœ¨ API Key æ ‡é¢˜åŒºæŠŠå€çŽ‡æŽ§ä»¶æ”¾åˆ°æœç´¢æ¡†æ—**

å°†çŽ°æœ‰å•ç‹¬çš„ `styles.apiSearchBar` å®¹å™¨æ›¿æ¢ä¸ºï¼š

```tsx
<div className={styles.apiKeyToolbar}>
  <label className={styles.apiKeyMultiplierControl}>
    <span>{t('usage_analytics.api_key_multiplier_setting')}</span>
    <input
      aria-label={t('usage_analytics.api_key_multiplier_setting')}
      type="number"
      inputMode="decimal"
      min="0"
      step="0.01"
      value={apiKeyCostMultiplierInput}
      onChange={(event) => setApiKeyCostMultiplierInput(event.target.value)}
      onBlur={() =>
        setApiKeyCostMultiplierInput(String(apiKeyCostMultiplier))
      }
    />
  </label>
  <div className={styles.apiSearchBar}>
    <IconSearch size={16} />
    <input
      value={usage.filters.apiKeyKeyword}
      onChange={(event) => updateFilters({ apiKeyKeyword: event.target.value })}
      placeholder={t('usage_analytics.api_key_keyword_placeholder')}
    />
  </div>
</div>
```

- [x] **Step 3: å°†å€çŽ‡åªä¼ ç»™ apiKeys çš„ RankTable**

åœ¨ apiKeys åˆ†æ”¯çš„ `<RankTable>` å¢žåŠ ï¼š

```tsx
displayCostMultiplier={apiKeyCostMultiplier}
```

ä¸è¦ç»™ model æˆ– credential åˆ†æ”¯ä¼ è¯¥å±žæ€§ã€‚

- [x] **Step 4: æ‰©å±• RankTable å‚æ•°å¹¶ä»¥å†…è”æ‹¬å·æ¸²æŸ“**

åœ¨ `RankTable` å‚æ•°ç±»åž‹ä¸­åŠ å…¥ `displayCostMultiplier?: number`ï¼Œå¹¶åœ¨æˆæœ¬å•å…ƒæ ¼ä½¿ç”¨ä»¥ä¸‹é€»è¾‘ï¼š

```tsx
const originalCost = formatMetricValue('estimatedCost', row.estimatedCost);
const multiplierCost =
  type === 'apiKey' &&
  displayCostMultiplier !== undefined &&
  Number.isFinite(row.estimatedCost)
    ? formatMetricValue('estimatedCost', row.estimatedCost * displayCostMultiplier)
    : null;
```

éšåŽå°†åŽŸæ¥çš„æˆæœ¬ `<td>` æ›¿æ¢ä¸ºï¼š

```tsx
<td>
  {originalCost}
  {multiplierCost !== null ? (
    <span className={styles.multiplierCost}>
      ï¼ˆ{t('usage_analytics.api_key_multiplier_amount', { value: multiplierCost })}ï¼‰
    </span>
  ) : null}
</td>
```

`row.estimatedCost` åªè¯»ä¸æ”¹ï¼›å€çŽ‡é‡‘é¢ä¸å‚ä¸ŽæŽ’åºã€å æ¯”ã€æ‘˜è¦æˆ–è¯¦æƒ…æ•°æ®ã€‚

- [x] **Step 5: è¿è¡Œé¡µé¢æµ‹è¯•ç¡®è®¤é€šè¿‡**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/UsageAnalyticsPage.test.tsx --testNamePattern="multiplier"
```

Expected: PASSï¼›é»˜è®¤ `1`ã€`0.3` å³æ—¶é‡ç®—ã€ç©ºå€¼å¤±ç„¦å›žåˆ° `1`ã€éž apiKeys é¡µé¢ä¸æ˜¾ç¤ºæŽ§ä»¶å‡é€šè¿‡ã€‚

### Task 5: å®Œæˆå•è¡Œå¸ƒå±€ä¸Žå››å¥—è¯­è¨€æ–‡æ¡ˆ

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.module.scss`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [x] **Step 1: å¢žåŠ å·¥å…·æ ä¸Žé‡‘é¢æ ·å¼**

åœ¨çŽ°æœ‰ `.apiSearchBar` æ ·å¼é™„è¿‘åŠ å…¥ç­‰ä»·çš„ CSS Module è§„åˆ™ï¼š

```scss
.apiKeyToolbar {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 10px;
  flex: 1 1 420px;
  min-width: 0;
  flex-wrap: wrap;
}

.apiKeyMultiplierControl {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: var(--usage-muted);
  font-size: 12px;
  font-weight: 700;
  white-space: nowrap;

  input {
    width: 76px;
    min-height: 38px;
    padding: 0 10px;
    border: 1px solid var(--usage-border);
    border-radius: 10px;
    background: var(--usage-surface);
    color: var(--usage-text);
    font: inherit;
  }
}

.multiplierCost {
  margin-left: 6px;
  color: var(--usage-muted);
  font-size: 12px;
  font-weight: 600;
}
```

åœ¨ `@media (max-width: 760px)` ä¸­è®© toolbar å æ»¡å¯ç”¨å®½åº¦å¹¶ä¿æŒæœç´¢æ¡†å¯ç¼©æ”¾ï¼š

```scss
.apiKeyToolbar {
  width: 100%;
  justify-content: flex-start;
}

.apiSearchBar {
  flex: 1 1 220px;
}
```

ä¸å¾—è®©å€çŽ‡é‡‘é¢æ¢æˆç¬¬äºŒè¡Œï¼›æ‹¬å·å†…å®¹ç»§ç»­åœ¨åŒä¸€ä¸ª `<td>` å†…è”æ˜¾ç¤ºã€‚

- [x] **Step 2: æ·»åŠ å››å¥— locale é”®**

åœ¨æ¯ä¸ª `usage_analytics` å¯¹è±¡ä¸Ž `api_key_keyword_placeholder` ç›¸é‚»å¤„åŠ å…¥åŒåé”®ï¼š

```json
"api_key_multiplier_setting": "å€çŽ‡è®¾ç½®",
"api_key_multiplier_amount": "å€çŽ‡ {{value}}"
```

å¯¹åº”ç¿»è¯‘ï¼š

```text
zh-TW: å€çŽ‡è¨­å®š / å€çŽ‡ {{value}}
en: Multiplier / Multiplier {{value}}
ru: ÐœÐ½Ð¾Ð¶Ð¸Ñ‚ÐµÐ»ÑŒ / ÐœÐ½Ð¾Ð¶Ð¸Ñ‚ÐµÐ»ÑŒ {{value}}
```

ä¿æŒå››ä¸ª JSON æ–‡ä»¶è¯­æ³•æœ‰æ•ˆã€é”®åå®Œå…¨ä¸€è‡´ã€‚

- [x] **Step 3: è¿è¡Œé¡µé¢æµ‹è¯•å’Œç±»åž‹æ£€æŸ¥**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/UsageAnalyticsPage.test.tsx src/features/usage-analytics/usageAnalyticsModel.test.ts
npm --workspace apps/web run type-check
```

Expected: ç›¸å…³ Vitest æµ‹è¯• PASSï¼ŒTypeScript æ— æ–°å¢žé”™è¯¯ã€‚

### Task 6: å›žå½’éªŒè¯ã€å·®å¼‚å®¡è®¡ä¸Žæäº¤

**Files:**
- Verify only the files listed above are part of this feature commit.

- [x] **Step 1: è¿è¡Œ Usage Analytics å…¨é‡æµ‹è¯•**

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics
```

Expected: è¯¥ç›®å½•æµ‹è¯•å…¨éƒ¨ PASSï¼›æ— æœªå¤„ç†çš„ Reactã€i18n æˆ– TypeScript é”™è¯¯ã€‚

- [x] **Step 2: è¿è¡Œå‰ç«¯ lint å’Œç”Ÿäº§æž„å»º**

```powershell
npm --workspace apps/web run lint
npm run build
```

Expected: lint æ—  errorï¼ŒVite/tsc ç”Ÿäº§æž„å»ºæˆåŠŸã€‚

- [x] **Step 3: å®¡è®¡å˜æ›´è¾¹ç•Œ**

```powershell
git status --short
git diff -- apps/web/src/features/usage-analytics apps/web/src/i18n/locales
```

ç¡®è®¤ï¼šæ²¡æœ‰æ–°å¢žæœåŠ¡ç«¯è¯·æ±‚ã€æ²¡æœ‰ä¿®æ”¹ `row.estimatedCost`ã€æ²¡æœ‰è§¦ç¢° CPA Coreã€æ²¡æœ‰æŠŠ `apps/manager-server/**` çš„æ—¢æœ‰ WIP æ··å…¥æš‚å­˜åŒºã€‚

- [x] **Step 4: åˆ›å»ºåŠŸèƒ½æäº¤**

```powershell
git add apps/web/src/features/usage-analytics/UsageAnalyticsPage.tsx `
  apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx `
  apps/web/src/features/usage-analytics/UsageAnalyticsPage.module.scss `
  apps/web/src/features/usage-analytics/usageAnalyticsModel.ts `
  apps/web/src/features/usage-analytics/usageAnalyticsModel.test.ts `
  apps/web/src/i18n/locales/zh-CN.json `
  apps/web/src/i18n/locales/zh-TW.json `
  apps/web/src/i18n/locales/en.json `
  apps/web/src/i18n/locales/ru.json
git commit -m "feat: show client key cost multiplier inline"
```

æäº¤å‰å†æ¬¡ç¡®è®¤æš‚å­˜åŒºä¸åŒ…å«ä»»ä½•æ—¢æœ‰æœåŠ¡å™¨ WIPï¼›æœ¬åŠŸèƒ½ä¸éƒ¨ç½²ï¼Œé™¤éžç”¨æˆ·å¦è¡Œæ˜Žç¡®è¦æ±‚ã€‚

