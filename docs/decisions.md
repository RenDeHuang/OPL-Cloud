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

## 2026-07-14: One Production Verifier

The paid verifier is the only release-gating commercial verifier and covers the
complete public product chain and exact cleanup.
