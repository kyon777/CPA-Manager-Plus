# Proxy Replacement zh-CN Copy Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore readable Simplified Chinese copy for the inspection-origin proxy-replacement modal and prevent literal question-mark placeholders from returning.

**Architecture:** The defect is isolated to eight values under `proxy_replacement` in `zh-CN.json`; the UI already renders those keys through i18next. Add a locale-resource regression test that asserts the intended rendered values and rejects `?`, then restore only those JSON values. No request, proxy replacement, recheck, quota, billing, or CPA Core behavior changes.

**Tech Stack:** TypeScript, Vitest, Vite JSON imports, i18next locale resource.

---

### Task 1: Add a zh-CN proxy-replacement copy regression and restore the damaged resource values

**Files:**
- Modify: `apps/web/src/i18n/locales/zh-CN.test.ts`
- Modify: `apps/web/src/i18n/locales/zh-CN.json`

- [x] **Step 1: Write the failing locale-resource test**

Append a `zh-CN proxy replacement copy` suite that reads `zhCN.proxy_replacement`, asserts the eight values below are free of `?`, and asserts the exact copy for the inspection explanation:

```ts
const fields = [
  copy.inspection_hint,
  copy.inspection_recheck,
  copy.mapping_account,
  copy.mapping_old_proxy,
  copy.mapping_new_proxy,
  copy.mapping_recheck,
  copy.completed_summary,
  copy.item_status_recheck_inconclusive,
];

expect(fields.some((field) => field.includes('?'))).toBe(false);
expect(copy.inspection_hint).toBe(
  '仅向当前代理经直接出口 IP 检查失败的凭证分配代理池中的正常代理。写入后，仅写入成功的凭证会使用新代理进行一次直接出口 IP 复检。只有直接出口 IP 检查失败才会标记为代理异常；结果未确定不会标记。不会自动更换或重试。'
);
```

- [x] **Step 2: Run the focused test and verify RED**

Run:

```powershell
npm --workspace apps/web run test -- src/i18n/locales/zh-CN.test.ts
```

Expected: FAIL because the existing eight resource strings include literal `?`.

- [x] **Step 3: Restore only the eight corrupted `proxy_replacement` strings**

Set the values in `zh-CN.json` to:

```json
"inspection_hint": "仅向当前代理经直接出口 IP 检查失败的凭证分配代理池中的正常代理。写入后，仅写入成功的凭证会使用新代理进行一次直接出口 IP 复检。只有直接出口 IP 检查失败才会标记为代理异常；结果未确定不会标记。不会自动更换或重试。",
"inspection_recheck": "写入成功的凭证会通过新代理进行一次直接出口 IP 检查。只有直接检查失败才会标记为代理异常；结果未确定不会标记。",
"mapping_account": "账号",
"mapping_old_proxy": "原代理 URL",
"mapping_new_proxy": "新代理 URL",
"mapping_recheck": "出口 IP 复检",
"completed_summary": "写入成功 {{updated}}，写入失败 {{failed}}，跳过 {{skipped}}；直接出口 IP 复检成功 {{recheckSuccess}}，失败 {{recheckFailed}}，结果未确定 {{recheckInconclusive}}。",
"item_status_recheck_inconclusive": "出口 IP 复检结果未确定（未标记为代理异常）"
```

- [x] **Step 4: Run focused test and type check**

Run:

```powershell
npm --workspace apps/web run test -- src/i18n/locales/zh-CN.test.ts
npm --workspace apps/web run type-check
```

Expected: both commands exit 0.

- [x] **Step 5: Build the actual production web bundle and inspect the emitted static HTML**

Run:

```powershell
npm --workspace apps/web run build
Select-String -LiteralPath apps/web/dist/management.html -Pattern 'inspection_hint:`\\?'
```

Expected: build exits 0; the static bundle contains the restored Chinese text and no literal `inspection_hint` question-mark value.

- [x] **Step 6: Commit the isolated fix**

```powershell
git add apps/web/src/i18n/locales/zh-CN.json apps/web/src/i18n/locales/zh-CN.test.ts docs/superpowers/plans/2026-11-11-proxy-replacement-zh-cn-copy-repair.md
git commit -m "fix: restore proxy replacement zh-CN copy"
```