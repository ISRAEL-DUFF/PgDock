# Webhooks and scheduled jobs

## Webhooks

A webhook POSTs the changes of chosen tables to a URL: Project →
Webhooks, `pgdock webhooks create`, or `POST /api/v1/projects/{id}/webhooks`.

| Field | Notes |
| --- | --- |
| Name | Letters, digits, spaces, dots, dashes, underscores. |
| Tables | One or more of the project's tables (`orders`, `billing.invoices`). |
| Events | Any of `INSERT`, `UPDATE`, `DELETE`. |
| Columns | Optional: an `UPDATE` fires only when one of these columns changed. |
| URL | `https://` to a public address. Plain `http://` and internal addresses only to hosts the platform admin allow-listed for your organisation. |
| Headers | Optional static headers (an auth header for your receiver); stored encrypted and never shown again. |
| Signing secret | Generated, shown once, rotatable. |

### How it works

Saving the first webhook installs a `pgdock` schema in the project's
database, owned by PGDock (your role can't read or change it), with an
outbox table and a trigger function. Each configured table gets triggers
that write every change to the outbox **in the same transaction**: a
rolled-back change never produces an event, and a committed one always
does. PGDock's delivery worker wakes on each commit (and checks every five
seconds), and posts the events of each webhook in commit order.

The `pgdock` schema name is reserved: a schema of that name you created is
replaced. Your role owns its tables, so it can drop PGDock's triggers;
PGDock notices and marks the webhook **broken** rather than putting them
back silently. Saving the webhook again reinstalls them.

### Payload

```json
{
  "id": "evt_3f2a9c1b7d4e_42",
  "webhook": "orders-to-slack",
  "project": "p_k2f9a7bq3d",
  "table": "public.orders",
  "type": "UPDATE",
  "record": { "id": 7, "status": "paid" },
  "old_record": { "id": 7, "status": "new" },
  "committed_at": "2026-10-01T12:00:00Z"
}
```

`record` is `null` for a `DELETE`, `old_record` for an `INSERT`. A change
whose rows serialise to more than 256 KB is sent with both omitted,
`"truncated": true` and the `primary_key`, so your receiver can fetch the
row. **Send test event** posts `"type": "TEST"`.

Each request carries `PGDock-Event-Id`, `PGDock-Webhook`, your static
headers, and `PGDock-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "t.body">`.

### Verifying the signature

Check the HMAC of `"<t>.<raw body>"` with the webhook's secret, compare in
constant time, and refuse timestamps more than five minutes away (replays).
De-duplicate on `PGDock-Event-Id`: delivery is at least once.

Go:

```go
func verify(secret, header string, body []byte, now time.Time) bool {
	var ts, sig string
	for _, part := range strings.Split(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "t":
			ts = v
		case "v1":
			sig = v
		}
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || math.Abs(now.Sub(time.Unix(n, 0)).Minutes()) > 5 {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	return hmac.Equal([]byte(hex.EncodeToString(m.Sum(nil))), []byte(sig))
}
```

Node:

```js
import { createHmac, timingSafeEqual } from "node:crypto";

function verify(secret, header, rawBody, now = Date.now()) {
  const parts = Object.fromEntries(header.split(",").map((kv) => kv.split("=")));
  if (!parts.t || !parts.v1 || Math.abs(now / 1000 - Number(parts.t)) > 300) return false;
  const want = createHmac("sha256", secret).update(`${parts.t}.${rawBody}`).digest("hex");
  return want.length === parts.v1.length && timingSafeEqual(Buffer.from(want), Buffer.from(parts.v1));
}
```

Verify the raw body, before parsing it.

### Delivery

- **Ordered per webhook**, in commit order. A failing event holds back the
  ones behind it for that webhook only.
- **Retries** on network errors, timeouts (10 s), `429` and `5xx`, with
  exponential backoff (10 s doubling to 2 h, with jitter) for 24 hours;
  then the event becomes a **dead letter**. Other `4xx` answers, and
  redirects (never followed), dead-letter at once.
- **Dead letters** stay 30 days: replay them one by one or all at once
  (`pgdock webhooks replay`); they are sent after the events waiting.
- After **50 failures in a row** the webhook is paused and the project's
  admins and the organisation's owners are emailed. A paused webhook
  records no new changes (its triggers are disabled); resuming sends what
  was queued.
- More than **100,000** queued events email the project's admins.
- Your plan limits **deliveries per minute** (Personal 60, Team 300).
  Beyond it events wait in the queue; none are dropped.
- The **delivery log** (status, latency, the first 4 KB of the answer) is
  kept 7 days.

While a project is promoted, demoted, restored or reset, delivery pauses
and resumes afterwards; the outbox moves with the data. Branches and
projects restored from a backup don't get the webhooks. A restore in place
reinstalls the triggers with an empty queue: events from the restored past
aren't sent. Deleting the project deletes its webhooks.

## Scheduled jobs

A job runs SQL, or calls a URL, on a schedule: Project → Jobs,
`pgdock jobs create`, or `POST /api/v1/projects/{id}/jobs`.

| Field | Notes |
| --- | --- |
| Schedule | A 5-field cron expression (`minute hour day month weekday`, with ranges, lists, steps, `jan`–`dec`, `sun`–`sat`, and `@hourly`, `@daily`, `@weekly`, `@monthly`), in a time zone (default UTC). |
| SQL | Runs as the project owner, in one transaction, with the timeout as `statement_timeout`. A storage soft lock (read-only) applies to it. |
| HTTP | Method, URL, headers, body; the same address rules as webhooks, signed with the job's secret (shown once), with a `PGDock-Job` header. |
| Timeout | Default 5 min for SQL, 30 s for HTTP; at most 1 hour. |
| Overlap | Skip a run while the previous one is going (default), or queue one. |

- Your plan limits the number of jobs (Personal 20, Team 50), how often
  one may run (every 5 minutes on Personal, every minute on Team), and HTTP
  job runs per hour (120, 600; runs beyond it are skipped).
- **Missed runs are not caught up**: after PGDock was down, the next
  scheduled time runs and the history shows the gap.
- Runs are skipped (and recorded as such) while the project is moving
  between tiers, restoring or resetting, while the organisation is
  suspended, and, for HTTP jobs, while its outbound traffic is off.
- **History**: start, duration, status, rows affected or HTTP status, and
  the error; kept 30 days, at most 1,000 runs per job. **Run now** starts
  one at once. Three failures in a row email the project's admins.
