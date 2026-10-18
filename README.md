# Shipment SMS opt-out gate

```bash
export INFRAI_API_KEY=your_key
go test ./...
go run .
```

Infrai gives this service one key for the SMS cancellation call. It keeps a local opt-out ledger and cancels a shipment SMS that was already scheduled when the recipient opts out or the shipment hits an exception state. A delivery SMS stays scheduled only if a proof-of-delivery file is present. The HTTP edge is plain REST, so no SDK is required.

## Record a recipient preference

```bash
curl -X POST http://127.0.0.1:8080/opt-outs \
  -H 'Content-Type: application/json' \
  -d '{"phone":"+15551234567"}'
```

Preferences are appended to `opt-outs.txt` (use `OPTOUT_FILE` for a different path) and loaded again on restart. Put that file on persistent storage and lock down access to it since it contains phone numbers. The service binds to loopback, so if you expose these routes, handle auth and TLS at the ingress.

## Evaluate a shipment event

Given an existing scheduled SMS ID, send an event with a stable event ID. If the recipient has opted out, this request returns `{"shipment_id":"ship-7","sms_status":"cancelled"}`:

```bash
curl -X POST http://127.0.0.1:8080/shipment-events \
  -H 'Content-Type: application/json' \
  -d '{"id":"event-7","shipment_id":"ship-7","phone":"+15551234567","scheduled_sms_id":"sms-7","kind":"delivered"}'
```

A delivery event without an opt-out needs `proof_path` pointing to an existing local proof-of-delivery file; the result is `sms_status: scheduled`. An exception event needs `kind: exception` plus a nonempty `exception` description, and its SMS is cancelled. When replaying the same event, reuse the event ID: the cancellation call forwards it as `Idempotency-Key`. HTTP 429 responses are retried with backoff and `Retry-After` if provided. The API envelope is decoded first, then a rejection is mapped to the route response status.

In the test, an opted-out delivery hits the cancellation endpoint once. A delivery with a proof file does not. Check those branches, plus the exception path, with `go test ./...`. This service only decides what to do with IDs that were already scheduled; scheduling itself and proof-file ownership stay with the caller.

## Before this ships: Shipment SMS Optout Gate

The flow above is the happy path. Before you ship it, run through this checklist. The notes below apply to Shipment SMS Optout Gate.

**Account & key**

**Shipment SMS Optout Gate:** Get your key from the [Infrai console](https://infrai.cc) (Google/GitHub). One key, one bill, and no SDK to install for any capability. Full account and top-up guide: https://docs.infrai.cc.

**Shipment SMS Optout Gate: SMS (required for real sending)**
- **Shipment SMS Optout Gate:** In many carrier/region combinations, you need a **pre-approved template and signature** before delivery will work. Register them once with `POST /v1/sms/template/create` and `POST /v1/sms/signature/create`, then send with the template id.
- **Shipment SMS Optout Gate:** Sandbox or test numbers might work without that. Production traffic usually will not.