export type CodexCreditsAvailability = {
  creditsHasCredits?: boolean | null;
  creditsUnlimited?: boolean | null;
  creditsBalance?: string | number | null;
  creditsOverageLimitReached?: boolean | null;
  spendControlReached?: boolean | null;
};

const parsePositiveCreditBalance = (value: string | number | null | undefined): boolean => {
  if (typeof value === 'number') return Number.isFinite(value) && value > 0;
  if (typeof value !== 'string') return false;
  const normalized = value.trim().replace(/,/g, '');
  if (!normalized) return false;
  const balance = Number(normalized);
  return Number.isFinite(balance) && balance > 0;
};

/**
 * Codex exposes included quota and paid credits independently. A positive
 * paid-credit balance is usable unless the Provider explicitly reports a
 * credit overage or spend-control block.
 */
export const hasUsableCodexCredits = (
  credits: CodexCreditsAvailability | null | undefined
): boolean => {
  if (!credits) return false;
  if (credits.creditsOverageLimitReached === true || credits.spendControlReached === true) {
    return false;
  }
  return (
    credits.creditsUnlimited === true ||
    credits.creditsHasCredits === true ||
    parsePositiveCreditBalance(credits.creditsBalance)
  );
};
