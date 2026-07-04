import { money } from "./core-utils.js";

export function storageHoldAmount({ packagePlan, pricing }) {
  return packageHoldAmount({ packagePlan, pricing }).storage;
}

export function pricingMarkup(pricing) {
  return pricing.markup ?? 0.2;
}

export function computeHourlyBase({ packagePlan, pricing }) {
  return pricing.computeHourly?.[packagePlan.id] ?? pricing.serverHourly?.[packagePlan.id] ?? 0;
}

export function storageGbMonthBase(pricing) {
  return pricing.storageGbMonth ?? pricing.diskGbMonth ?? 0.2;
}

export function pricedComputeHourly({ packagePlan, pricing }) {
  return money(computeHourlyBase({ packagePlan, pricing }) * (1 + pricingMarkup(pricing)));
}

export function pricedStorageGbMonth(pricing) {
  return money(storageGbMonthBase(pricing) * (1 + pricingMarkup(pricing)));
}

export function packageHoldAmount({ packagePlan, pricing }) {
  const compute = money(pricedComputeHourly({ packagePlan, pricing }) * 24 * 7);
  const storage = money((packagePlan.diskGb * pricedStorageGbMonth(pricing) / 30) * 7);
  return {
    compute,
    storage,
    total: money(compute + storage)
  };
}

export function hourlyStorageAmount({ packagePlan, pricing, hours }) {
  const gbMonth = storageGbMonthBase(pricing);
  const markup = pricingMarkup(pricing);
  return money((packagePlan.diskGb * gbMonth * (1 + markup) / 30 / 24) * hours);
}

export function storageGbHourPrice(pricing) {
  return money(storageGbMonthBase(pricing) * (1 + pricingMarkup(pricing)) / 30 / 24);
}

export function hourlyComputeAmount({ packagePlan, pricing, hours }) {
  const hourly = computeHourlyBase({ packagePlan, pricing });
  const markup = pricingMarkup(pricing);
  return money(hourly * (1 + markup) * hours);
}

export function billableHours(hours) {
  const value = Number(hours);
  if (!Number.isFinite(value) || value <= 0) throw new Error("positive_hours_required");
  return Math.ceil(value);
}

export function billingPolicy(pricing) {
  return {
    currency: "CNY",
    markup: pricingMarkup(pricing),
    prepaidHoldDays: 7,
    minimumBillableHours: 1,
    billingCadence: "hourly",
    fundingOrder: ["available_balance", "frozen_hold"],
    computeHoldExhaustion: "mark_compute_hold_exhausted",
    storageHoldExhaustion: "freeze_workspace_until_top_up_or_storage_destroy",
    storageDestroyConfirmation: "required"
  };
}
