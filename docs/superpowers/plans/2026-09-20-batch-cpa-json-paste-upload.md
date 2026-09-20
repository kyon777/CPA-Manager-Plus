# Batch CPA JSON Paste Upload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Allow the existing Paste JSON dialog to accept a top-level array of CPA auth records and upload each record as its own JSON auth file.

**Architecture:** Extend the client-side CPA converter to validate either one CPA record or an array of CPA records. Reuse the existing payload builder and `authFilesApi.uploadFiles()` path so each array item becomes a separate `File` and is sent through the current authenticated upload endpoint. No CPA Core or backend protocol changes are needed.

**Tech Stack:** React/TypeScript, Vitest, existing auth-file converter and upload API, JSON locale files.

---

### Task 1: Add failing converter tests for CPA arrays

**Files:**
- Modify: `apps/web/src/features/authFiles/sessionAuthConverter.test.ts`

- [ ] **Step 1: Write the failing tests**

Add tests in the existing `describe('convertAuthJsonInput', ...)` suite using token placeholders only:

```ts
it('accepts a top-level CPA array and preserves each record', () => {
  const records = [
    { type: 'codex', email: 'first@example.com', access_token: 'first-token' },
    { type: 'codex', email: 'second@example.com', access_token: 'second-token' },
  ];

  expect(convertAuthJsonInput(JSON.stringify(records), 'cpa')).toEqual(records);
});

it('rejects a CPA array member without auth fields before upload', () => {
  const records = [
    { type: 'codex', email: 'valid@example.com', access_token: 'valid-token' },
    { type: 'codex', email: 'invalid@example.com' },
  ];

  expect(() => convertAuthJsonInput(JSON.stringify(records), 'cpa')).toThrow(
    'CPA auth JSON item 2 is missing required auth fields'
  );
});
```

Add a `buildAuthJsonFilePayloads` test for the approved email-based names and duplicate handling:

```ts
it('splits a CPA array into unique email-based file payloads', () => {
  const records = [
    { type: 'codex', email: 'First.User@example.com', access_token: 'first-token' },
    { type: 'codex', email: 'First.User@example.com', access_token: 'second-token' },
  ];

  expect(buildAuthJsonFilePayloads('cpa', 'codex-account.json', JSON.stringify(records))).toEqual([
    { fileName: 'first.user@example.com.json', authJson: records[0] },
    { fileName: 'first.user@example.com-2.json', authJson: records[1] },
  ]);
});
```

- [ ] **Step 2: Run the focused converter tests and verify RED**

Run:

```powershell
npm --workspace apps/web run test -- src/features/authFiles/sessionAuthConverter.test.ts
```

Expected result before production changes: the CPA-array tests fail because the parser currently rejects a top-level array and the multi-record naming helper uses the old generated name.

### Task 2: Implement CPA-array validation and file naming

**Files:**
- Modify: `apps/web/src/features/authFiles/sessionAuthConverter.ts`

- [ ] **Step 1: Permit CPA arrays in the parser**

Change the `parseJsonObject` call in `convertAuthJsonInput` so CPA, session, and sub2api inputs may be arrays:

```ts
const parsed = parseJsonObject(
  text,
  type === 'cpa' || type === 'session' || type === 'sub2api',
  maxInputChars
);
```

- [ ] **Step 2: Validate every CPA array member atomically**

Replace the single-record CPA branch with logic that wraps a single object in a one-item list, rejects an empty array, rejects non-object members with an indexed error, checks unsafe `id_token` values, and checks `hasCpaAuthFileShape` for every member. Preserve the existing single-object error text:

```ts
if (type === 'cpa') {
  const cpaRecords = Array.isArray(parsed) ? parsed : [parsed];
  if (cpaRecords.length === 0) {
    throw new AuthJsonConversionError('CPA auth JSON array must contain at least one object');
  }

  const validatedRecords = cpaRecords.map((record, index) => {
    if (!isRecord(record)) {
      throw new AuthJsonConversionError(
        Array.isArray(parsed)
          ? `CPA auth JSON item ${index + 1} must be an object`
          : 'CPA auth JSON is missing required auth fields'
      );
    }
    if (hasUnsafeCpaIdToken(record)) {
      throw new AuthJsonConversionError(
        Array.isArray(parsed)
          ? `CPA auth JSON item ${index + 1} contains unsupported id_token`
          : 'CPA auth JSON contains unsupported id_token'
      );
    }
    if (!hasCpaAuthFileShape(record)) {
      throw new AuthJsonConversionError(
        Array.isArray(parsed)
          ? `CPA auth JSON item ${index + 1} is missing required auth fields`
          : 'CPA auth JSON is missing required auth fields'
      );
    }
    return record;
  });

  return Array.isArray(parsed) ? validatedRecords : validatedRecords[0];
}
```

- [ ] **Step 3: Add an email-based batch filename helper**

Add this helper immediately before `getDefaultSessionAuthFileName`:

```ts
const getDefaultCpaBatchAuthFileName = (authJson: JsonRecord) => {
  const identity = buildSafeFileNameSegment(
    firstNonEmpty(authJson.email, authJson.name, authJson.account_id, 'account'),
    {
      fallback: 'account',
      maxLength: 120,
      preserveEmailSymbols: true,
    }
  );

  return `${identity}.json`;
};
```

- [ ] **Step 4: Use the helper only for multi-record CPA paste**

In `buildAuthJsonFilePayloads`, keep the existing single-record behavior and change the multi-record mapping to select `getDefaultCpaBatchAuthFileName` for `type === 'cpa'`, otherwise retain `getDefaultSessionAuthFileName`. Continue using `ensureUniqueAuthJsonFilePayloadNames` for `-2`, `-3`, etc. suffixes.

- [ ] **Step 5: Run the focused converter tests and verify GREEN**

Run the same command from Task 1. Expected result: all converter tests pass.

### Task 3: Verify the existing hook uploads one physical file per CPA record

**Files:**
- Modify: `apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts`

- [ ] **Step 1: Add a failing integration test**

Add a test next to the existing pasted CPA tests. Use three records with non-secret placeholder tokens, mock `uploadFiles` to return all file names, call `savePastedAuthJson('cpa', 'codex-account.json', JSON.stringify(records))`, and assert:

```ts
expect(mocks.saveJsonObject).not.toHaveBeenCalled();
expect(mocks.uploadFiles).toHaveBeenCalledTimes(1);
expect((mocks.uploadFiles.mock.calls[0]?.[0] as File[]).map((file) => file.name)).toEqual([
  'first@example.com.json',
  'second@example.com.json',
  'third@example.com.json',
]);
```

Also read each uploaded `File` with `await file.text()` and assert each text parses to the corresponding original object. This proves the array is not uploaded as one array file and that records are not rewritten.

- [ ] **Step 2: Run the focused hook test and verify RED**

Run:

```powershell
npm --workspace apps/web run test -- src/features/authFiles/hooks/useAuthFilesData.test.ts
```

Expected result before the converter change: the new test fails because CPA arrays are rejected.

- [ ] **Step 3: Run the hook test after Task 2 and verify GREEN**

Run the same command and confirm the existing paste, partial-upload, retry, and reload tests remain passing.

### Task 4: Document the array input in the paste dialog

**Files:**
- Modify: `apps/web/src/i18n/locales/zh-CN.json`
- Modify: `apps/web/src/i18n/locales/en.json`
- Modify: `apps/web/src/i18n/locales/ru.json`
- Modify: `apps/web/src/i18n/locales/zh-TW.json`

- [ ] **Step 1: Update the CPA placeholder and hint in all locales**

Keep translation keys stable, but update `paste_cpa_placeholder` to show that either an object or an array is accepted and update `paste_cpa_hint` to explain that a top-level array is split into one file per account, named from email. Do not include real credentials or token values.

Chinese Simplified text:

```json
"paste_cpa_placeholder": "[
  {
    \"type\": \"codex\",
    \"email\": \"account@example.com\",
    \"access_token\": \"...\"
  }
]",
"paste_cpa_hint": "支持单个 CPA JSON 或顶层数组；数组中的每个账号会按邮箱拆分为独立 JSON 文件后自动上传。"
```

Use equivalent wording in English, Russian, and Traditional Chinese while preserving valid JSON escaping.

- [ ] **Step 2: Run locale parsing and focused UI tests**

Run:

```powershell
node -e "for (const f of ['zh-CN','en','ru','zh-TW']) JSON.parse(require('fs').readFileSync('apps/web/src/i18n/locales/' + f + '.json','utf8')); console.log('locale JSON ok')"
npm --workspace apps/web run test -- src/features/authFiles/components/AuthJsonPasteModal.test.tsx
```

Expected result: locale parsing succeeds and the modal test suite remains green.

### Task 5: Full verification and commit

**Files:**
- No additional source files.

- [ ] **Step 1: Run type-check**

```powershell
npm run type-check
```

Expected: exit code 0.

- [ ] **Step 2: Run the complete web test suite**

```powershell
npm run test:web
```

Expected: exit code 0 with no new failures. If an unrelated pre-existing failure appears, record its exact test and output instead of masking it.

- [ ] **Step 3: Build the web application**

```powershell
npm run build
```

Expected: exit code 0.

- [ ] **Step 4: Review the diff for secret leakage and scope**

```powershell
git diff --check
git diff --stat
git diff -- apps/web/src/features/authFiles/sessionAuthConverter.ts apps/web/src/features/authFiles/sessionAuthConverter.test.ts apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts apps/web/src/i18n/locales
```

Confirm no real token, API key, or pasted credential appears in tracked files or test output.

- [ ] **Step 5: Commit only to the custom branch**

```powershell
git add apps/web/src/features/authFiles/sessionAuthConverter.ts apps/web/src/features/authFiles/sessionAuthConverter.test.ts apps/web/src/features/authFiles/hooks/useAuthFilesData.test.ts apps/web/src/i18n/locales docs/superpowers/plans/2026-09-20-batch-cpa-json-paste-upload.md
git commit -m "feat: batch upload pasted CPA JSON records"
```

Do not push `upstream`; if pushing is requested later, use only `origin lo/custom`.
