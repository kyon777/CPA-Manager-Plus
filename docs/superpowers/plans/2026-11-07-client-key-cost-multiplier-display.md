# 客户端 Key 预估成本倍率显示 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Usage Analytics 的“客户端 Key 消耗排行”表格中增加一个纯前端倍率输入，并在现有预估成本金额后以内联括号显示倍率金额。

**Architecture:** 在 `UsageAnalyticsPageInner` 保存输入框原始字符串，使用 `usageAnalyticsModel.ts` 中的纯函数将空值、非法值和负数归一化为 `1`。仅把归一化后的展示倍率传入 `RankTable` 的 `apiKey` 分支；`RankTable` 保留原始 `row.estimatedCost`，只在渲染同一个成本单元格时追加倍率金额。所有数据请求、排序、摘要和计费字段保持不变。

**Tech Stack:** React 19、TypeScript、React Test Renderer、Vitest、CSS Modules、i18next。

---

## 文件改动清单

- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.ts` — 增加可单测的倍率归一化纯函数；不增加服务端模型字段。
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.test.ts` — 覆盖默认值、合法小数、零值和非法输入。
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.tsx` — 增加当前页面状态、倍率控件、`RankTable` 展示参数和内联金额渲染。
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx` — 覆盖默认显示、即时重算、失焦归一化、仅 apiKeys 表格生效和不触发请求。
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.module.scss` — 增加倍率控件与括号金额的紧凑单行布局，不改变其他表格样式。
- Modify: `apps/web/src/i18n/locales/zh-CN.json` — 增加倍率设置和倍率金额文案。
- Modify: `apps/web/src/i18n/locales/zh-TW.json` — 增加繁体中文文案。
- Modify: `apps/web/src/i18n/locales/en.json` — 增加英文文案。
- Modify: `apps/web/src/i18n/locales/ru.json` — 增加俄文文案。

所有命令均从 `C:\kyon\docker\cpa-manager\source` 执行。当前 `apps/manager-server/**` 的既有 WIP 不得加入本功能提交。

### Task 1: 为倍率输入定义归一化契约（RED）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.test.ts`

- [ ] **Step 1: 添加失败测试**

在现有 model 测试 import 中加入 `normalizeUsageDisplayMultiplier`，并在测试文件中加入：

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

- [ ] **Step 2: 运行测试确认是预期失败**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/usageAnalyticsModel.test.ts --testNamePattern="usage display multiplier"
```

Expected: FAIL because `normalizeUsageDisplayMultiplier` has not been exported yet；不得通过修改测试来绕过缺失导出。

### Task 2: 实现并验证倍率归一化纯函数（GREEN）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/usageAnalyticsModel.ts`
- Test: `apps/web/src/features/usage-analytics/usageAnalyticsModel.test.ts`

- [ ] **Step 1: 添加最小实现**

在现有 `formatMetricValue` 附近导出以下函数：

```ts
export const normalizeUsageDisplayMultiplier = (value: string): number => {
  const trimmed = value.trim();
  if (!trimmed) return 1;

  const parsed = Number(trimmed);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 1;
};
```

该函数只处理展示输入，不写入任何 Usage API 参数或后端模型。

- [ ] **Step 2: 运行同一测试确认通过**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/usageAnalyticsModel.test.ts --testNamePattern="usage display multiplier"
```

Expected: PASS，8 个用例全部通过。

### Task 3: 先写客户端 Key 表格行为测试（RED）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx`

- [ ] **Step 1: 添加测试辅助函数**

在现有 `getText`/`renderPage` 辅助函数附近添加按 aria-label 查找倍率输入和读取成本列的辅助逻辑：

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

- [ ] **Step 2: 添加失败测试，锁定用户可见行为**

在 `UsageAnalyticsPage.test.tsx` 的 apiKeys 测试附近加入：

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

- [ ] **Step 3: 运行测试确认是预期失败**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/UsageAnalyticsPage.test.tsx --testNamePattern="multiplier"
```

Expected: FAIL because the input, multiplier prop and inline rendering do not exist yet；失败原因应是找不到倍率输入，而不是测试环境启动错误。

### Task 4: 接入页面状态与 RankTable 展示（GREEN）

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.tsx`
- Test: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.test.tsx`

- [ ] **Step 1: 导入归一化函数并增加局部页面状态**

在现有 model import 中加入 `normalizeUsageDisplayMultiplier`，在 `UsageAnalyticsPageInner` 的其他 `useState` 附近加入：

```tsx
const [apiKeyCostMultiplierInput, setApiKeyCostMultiplierInput] = useState('1');
const apiKeyCostMultiplier = normalizeUsageDisplayMultiplier(apiKeyCostMultiplierInput);
```

输入状态只存在当前页面实例中；不写 localStorage、不写 URL、不发请求。

- [ ] **Step 2: 在 API Key 标题区把倍率控件放到搜索框旁**

将现有单独的 `styles.apiSearchBar` 容器替换为：

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

- [ ] **Step 3: 将倍率只传给 apiKeys 的 RankTable**

在 apiKeys 分支的 `<RankTable>` 增加：

```tsx
displayCostMultiplier={apiKeyCostMultiplier}
```

不要给 model 或 credential 分支传该属性。

- [ ] **Step 4: 扩展 RankTable 参数并以内联括号渲染**

在 `RankTable` 参数类型中加入 `displayCostMultiplier?: number`，并在成本单元格使用以下逻辑：

```tsx
const originalCost = formatMetricValue('estimatedCost', row.estimatedCost);
const multiplierCost =
  type === 'apiKey' &&
  displayCostMultiplier !== undefined &&
  Number.isFinite(row.estimatedCost)
    ? formatMetricValue('estimatedCost', row.estimatedCost * displayCostMultiplier)
    : null;
```

随后将原来的成本 `<td>` 替换为：

```tsx
<td>
  {originalCost}
  {multiplierCost !== null ? (
    <span className={styles.multiplierCost}>
      （{t('usage_analytics.api_key_multiplier_amount', { value: multiplierCost })}）
    </span>
  ) : null}
</td>
```

`row.estimatedCost` 只读不改；倍率金额不参与排序、占比、摘要或详情数据。

- [ ] **Step 5: 运行页面测试确认通过**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/UsageAnalyticsPage.test.tsx --testNamePattern="multiplier"
```

Expected: PASS；默认 `1`、`0.3` 即时重算、空值失焦回到 `1`、非 apiKeys 页面不显示控件均通过。

### Task 5: 完成单行布局与四套语言文案

**Files:**
- Modify: `apps/web/src/features/usage-analytics/UsageAnalyticsPage.module.scss`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [ ] **Step 1: 增加工具栏与金额样式**

在现有 `.apiSearchBar` 样式附近加入等价的 CSS Module 规则：

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

在 `@media (max-width: 760px)` 中让 toolbar 占满可用宽度并保持搜索框可缩放：

```scss
.apiKeyToolbar {
  width: 100%;
  justify-content: flex-start;
}

.apiSearchBar {
  flex: 1 1 220px;
}
```

不得让倍率金额换成第二行；括号内容继续在同一个 `<td>` 内联显示。

- [ ] **Step 2: 添加四套 locale 键**

在每个 `usage_analytics` 对象与 `api_key_keyword_placeholder` 相邻处加入同名键：

```json
"api_key_multiplier_setting": "倍率设置",
"api_key_multiplier_amount": "倍率 {{value}}"
```

对应翻译：

```text
zh-TW: 倍率設定 / 倍率 {{value}}
en: Multiplier / Multiplier {{value}}
ru: Множитель / Множитель {{value}}
```

保持四个 JSON 文件语法有效、键名完全一致。

- [ ] **Step 3: 运行页面测试和类型检查**

Run:

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics/UsageAnalyticsPage.test.tsx src/features/usage-analytics/usageAnalyticsModel.test.ts
npm --workspace apps/web run type-check
```

Expected: 相关 Vitest 测试 PASS，TypeScript 无新增错误。

### Task 6: 回归验证、差异审计与提交

**Files:**
- Verify only the files listed above are part of this feature commit.

- [ ] **Step 1: 运行 Usage Analytics 全量测试**

```powershell
npm --workspace apps/web run test -- src/features/usage-analytics
```

Expected: 该目录测试全部 PASS；无未处理的 React、i18n 或 TypeScript 错误。

- [ ] **Step 2: 运行前端 lint 和生产构建**

```powershell
npm --workspace apps/web run lint
npm run build
```

Expected: lint 无 error，Vite/tsc 生产构建成功。

- [ ] **Step 3: 审计变更边界**

```powershell
git status --short
git diff -- apps/web/src/features/usage-analytics apps/web/src/i18n/locales
```

确认：没有新增服务端请求、没有修改 `row.estimatedCost`、没有触碰 CPA Core、没有把 `apps/manager-server/**` 的既有 WIP 混入暂存区。

- [ ] **Step 4: 创建功能提交**

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

提交前再次确认暂存区不包含任何既有服务器 WIP；本功能不部署，除非用户另行明确要求。
