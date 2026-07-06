import { money } from "./core-utils.ts";

export function storageHoldAmount({ packagePlan, pricing }) {
  return packageHoldAmount({ packagePlan, pricing }).storage;
}

export function userComputeHourly({ packagePlan, pricing }) {
  return money(pricing.computeHourly?.[packagePlan.id] ?? pricing.serverHourly?.[packagePlan.id] ?? 0);
}

export function userStorageGbMonth(pricing) {
  return money(pricing.storageGbMonth ?? pricing.diskGbMonth ?? 0);
}

export function providerCostEstimate({ packagePlan, pricing }) {
  const planEstimate = pricing.providerCostEstimate?.computeHourly?.[packagePlan.id] || {};
  return {
    billingUse: pricing.providerCostEstimate?.billingUse || "internal_reconciliation_only",
    source: pricing.providerCostEstimate?.source || "",
    sourceRegion: pricing.providerCostEstimate?.sourceRegion || "",
    instanceType: planEstimate.instanceType || packagePlan.instanceType || "",
    estimatedHourly: money(Number(planEstimate.estimatedHourly || 0))
  };
}

export function providerStorageCostEstimate(pricing) {
  const estimate = pricing.providerCostEstimate?.storageGbMonth || {};
  return {
    billingUse: pricing.providerCostEstimate?.billingUse || "internal_reconciliation_only",
    source: pricing.providerCostEstimate?.source || "",
    sourceRegion: pricing.providerCostEstimate?.sourceRegion || "",
    storageClass: estimate.storageClass || "",
    estimatedGbMonth: money(Number(estimate.estimatedGbMonth || 0))
  };
}

export function computePriceSnapshot({ packagePlan, pricing }) {
  return {
    priceBasis: pricing.priceBasis || "opl_user_price_catalog",
    userPrice: {
      computeHourly: userComputeHourly({ packagePlan, pricing }),
      currency: pricing.currency || "CNY"
    },
    providerCostEstimate: providerCostEstimate({ packagePlan, pricing })
  };
}

export function storagePriceSnapshot({ pricing, sizeGb }) {
  return {
    priceBasis: pricing.priceBasis || "opl_user_price_catalog",
    userPrice: {
      storageGbMonth: userStorageGbMonth(pricing),
      storageGbHour: storageGbHourPrice(pricing),
      sizeGb,
      currency: pricing.currency || "CNY"
    },
    providerCostEstimate: providerStorageCostEstimate(pricing)
  };
}

export function pricedComputeHourly({ packagePlan, pricing }) {
  return userComputeHourly({ packagePlan, pricing });
}

export function pricedStorageGbMonth(pricing) {
  return userStorageGbMonth(pricing);
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
  return money((packagePlan.diskGb * userStorageGbMonth(pricing) / 30 / 24) * hours);
}

export function storageGbHourPrice(pricing) {
  return money(userStorageGbMonth(pricing) / 30 / 24);
}

export function hourlyComputeAmount({ packagePlan, pricing, hours }) {
  return money(userComputeHourly({ packagePlan, pricing }) * hours);
}

export function billableHours(hours) {
  const value = Number(hours);
  if (!Number.isFinite(value) || value <= 0) throw new Error("positive_hours_required");
  return Math.ceil(value);
}

export function billingPolicy(pricing) {
  return {
    currency: "CNY",
    priceBasis: pricing.priceBasis || "opl_user_price_catalog",
    providerCostBasis: pricing.providerCostBasis || "internal_estimate_only",
    prepaidHoldDays: 7,
    minimumBillableHours: 1,
    billingCadence: "hourly",
    fundingOrder: ["available_balance", "frozen_hold"],
    computeHoldExhaustion: "mark_compute_hold_exhausted",
    storageHoldExhaustion: "freeze_workspace_until_top_up_or_storage_destroy",
    storageDestroyConfirmation: "required"
  };
}
