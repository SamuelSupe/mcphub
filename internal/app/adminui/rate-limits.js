import { t } from "./i18n.js";

const fields = ["requests_per_second", "burst", "max_concurrent"];

export function fillRateLimit(prefix, policy = {}) {
  for (const field of fields) document.querySelector(`#${prefix}-${field}`).value = policy[field] || 0;
}

export function collectRateLimit(prefix) {
  const policy = Object.fromEntries(fields.map((field) => [field, Number(document.querySelector(`#${prefix}-${field}`).value)]));
  if (fields.some((field) => !Number.isFinite(policy[field]) || policy[field] < 0) || !Number.isSafeInteger(policy.burst) || !Number.isSafeInteger(policy.max_concurrent)) {
    throw new Error(t("限流值必须为非负数，突发容量和最大并发必须为整数。"));
  }
  if (policy.burst > 0 && policy.requests_per_second === 0) throw new Error(t("设置突发容量前，请先设置每秒请求数。"));
  return policy;
}
