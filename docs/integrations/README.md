# Building an integration

For a tool that works with its users' Xenibase projects (a workflow
platform, a sync service, a dashboard). Everything here uses the public
contracts: [api/openapi.yaml](../../api/openapi.yaml) (the management API),
the [data API](../backend-services.md) and its per-project OpenAPI document
at `GET https://<ref>.<api-domain>/data/v1/openapi.json`,
[webhooks](../webhooks.md), and [error codes](../errors.md). What is
covered by the deprecation policy is at the end of
[errors.md](../errors.md#deprecation).

## Connecting

Xenibase is self-hosted: each install has its own base URL
(`https://xenibase.example.com/api/v1`) and API domain for projects
(`https://<ref>.api.example.com`). Let each connection set its own.

| For | Credential | Where the customer gets it |
| --- | --- | --- |
| Webhooks, project listing, the SQL endpoint for reads | An **API token** restricted to the projects the tool needs, scopes `read` and `write` | Account → API tokens, or `xenibase tokens create --name … --scopes read,write --project …` |
| Writing rows, calling functions | The project's **secret key** (`xb_sec_…`) for the data API | Project → API → API keys (backend services must be on) |

Send the token as `Authorization: Bearer xb_…`; token calls don't need
the CSRF header. Send the secret key as `apikey: xb_sec_…`, from your
servers only. Both are shown once when made; have the customer enter them
straight into your product, never by email or chat. A token that also has
the `admin` scope can create secret keys itself
(`POST /projects/{id}/services/keys`), but asking for less is better.

## Reacting to row changes

Create a webhook per trigger with the token, mark it with `metadata` so
you can find and repair it, verify every delivery's signature, and
de-duplicate on `Xenibase-Event-Id`. The contract is in
[webhooks.md](../webhooks.md#managing-webhooks-from-another-tool).

## Writing

Use the data API, not the SQL endpoint: values are bound, each call is one
operation, update and delete need a filter, `max_affected` caps the rows,
errors are non-2xx with a code, and an `Idempotency-Key` makes retries
safe. See [Writing data](../backend-services.md#writing-data).

## Samples to test against

[fixtures/webhooks](fixtures/webhooks) has one real, signed delivery of
each kind (`INSERT`, `UPDATE` with `old_record`, `DELETE`, a truncated
event over 256 KB, and `TEST`), each with:

```json
{
  "description": "…",
  "secret": "whsec_…",
  "headers": {"Xenibase-Signature": "t=…,v1=…", "Xenibase-Event-Id": "…", "Xenibase-Webhook": "…", "Content-Type": "application/json", "User-Agent": "Xenibase-Webhooks/1"},
  "body": "<the exact bytes that were signed>"
}
```

The secret is a throwaway, from a test install that no longer exists.
Verify `body` as is (don't re-serialise it), using the signature's own
timestamp as "now". Xenibase's CI checks that these samples still match what
it sends (`TestWebhookFixtures`), so a change of shape shows up here, and
in the changelog, first.
