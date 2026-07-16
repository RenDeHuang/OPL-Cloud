# Decisions

## 2026-07-14: Sub2API Is The Only Spendable Balance

`gflabtoken.cn` owns USD balance, API keys, routing, and request usage. Control
Plane reads balance and submits exact adjustments through the existing Sub2API
management API. Console displays that projection. Ledger stores evidence only.

## 2026-07-14: Resources Are Prepaid Monthly

Basic compute is CNY 350/month, Pro compute is CNY 1,500/month, and storage is
CNY 18 per 10 GB/month. Charges use fixed integer USD micros at `1 USD = 7 CNY`.
Tencent procurement and provider cost are internal evidence, not customer price.

## 2026-07-14: Control Plane Serves Console Product Commands Only

Control Plane orchestrates product outcomes. It does not expose generic Fabric,
Ledger, or Sub2API proxies and does not enter App, Workspace runtime, or MAS
direct call chains.

## 2026-07-14: Hard Cut On A Fresh Database

There are no users or production billing records to preserve. Old commercial
schemas, compatibility routes, duplicate write paths, and state importers are
deleted.

## 2026-07-14: Provider And Commercial State Have Different Owners

Fabric owns resource/provider facts. Control Plane owns monthly entitlements and
billing operations. Ledger owns append-only evidence. A Fabric response must not
replace Control Plane commercial fields.

## 2026-07-16: Reusable Verification Replaces Per-Run Paid Provisioning

The legacy paid verifier is blocked and is not a release gate. Ordinary CI and
commercial E2E use fake monthly settlement and provider mutations. Runtime E2E
reuses one prepaid `SA5.MEDIUM4` plus 10GB CBS Verification Slot for its paid
period and deletes only temporary workloads and test data. A real provider
purchase or renewal requires a separate explicit Provider Acceptance run.
