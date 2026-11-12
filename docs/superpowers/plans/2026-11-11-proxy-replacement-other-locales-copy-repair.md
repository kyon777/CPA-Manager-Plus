# Proxy Replacement Other Locale Copy Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore readable Traditional Chinese and Russian copy for the proxy-replacement inspection modal, matching the repaired Simplified Chinese surface.

**Architecture:** The same historical resource corruption left literal question-mark placeholders in `zh-TW.json` and `ru.json`. Add one locale-integrity test over the eight affected `proxy_replacement` values for all shipped locales, then restore only the sixteen damaged strings. No component, API, proxy, quota, billing, or CPA Core logic changes.

**Tech Stack:** TypeScript, Vitest, JSON locale resources.

---

### Task 1: Prevent question-mark placeholder copy in proxy-replacement locales

**Files:**
- Create: `apps/web/src/i18n/locales/proxyReplacement.locales.test.ts`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`
- Modify: `apps/web/src/i18n/locales/ru.json`

- [x] **Step 1: Write a failing integrity test**

Import `en.json`, `ru.json`, `zh-CN.json`, and `zh-TW.json`. For each locale, collect `inspection_hint`, `inspection_recheck`, `mapping_account`, `mapping_old_proxy`, `mapping_new_proxy`, `mapping_recheck`, `completed_summary`, and `item_status_recheck_inconclusive`; assert none contains a literal `?`.

- [x] **Step 2: Run the focused test and verify RED**

```powershell
npm --workspace apps/web run test -- src/i18n/locales/proxyReplacement.locales.test.ts
```

Expected: FAIL for `ru` and `zh-TW`; `en` and repaired `zh-CN` pass.

- [x] **Step 3: Restore only the damaged values**

Replace the sixteen literal question-mark resource values with UTF-8 Traditional Chinese and Russian translations that preserve the existing exit-IP check semantics.

- [x] **Step 4: Run focused locale/UI tests, type check, and production build**

```powershell
npm --workspace apps/web run test -- src/i18n/locales/zh-CN.test.ts src/i18n/locales/proxyReplacement.locales.test.ts src/features/accounts/components/ProxyReplacementModal.test.tsx
npm --workspace apps/web run type-check
npm --workspace apps/web run build
```

Expected: all commands exit 0.

- [x] **Step 5: Commit, fast-forward `lo/custom`, push `origin/lo/custom`, build the same Linux image, and deploy CPAM only**

Use the existing guarded local deployer and native-server atomic CPAM deployment flow. Verify public `management.html` contains all translated values and contains no literal proxy-replacement placeholder strings.