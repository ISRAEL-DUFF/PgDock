import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { useState } from "react";
import { api, errorMessage, type Payment } from "../api/client";
import { naira, parseNaira } from "../lib/billing";
import { formatDate } from "../lib/format";
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Panel,
  Select,
  SidePanel,
  Table,
} from "./ui";

/** Runs an action, keeping its busy flag and error. */
function useAction() {
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const run = async (key: string, f: () => Promise<unknown>) => {
    setBusy(key);
    setErr(null);
    try {
      await f();
      return true;
    } catch (e) {
      setErr(errorMessage(e));
      return false;
    } finally {
      setBusy(null);
    }
  };
  return { busy, err, run };
}

/** Payments across providers, manual entries and refunds (V3 §3.5). */
export function PaymentsTab() {
  const [provider, setProvider] = useState("");
  const [recording, setRecording] = useState(false);
  const [refunding, setRefunding] = useState<Payment | null>(null);
  const q = useQuery({
    queryKey: ["admin-payments", provider],
    queryFn: () => api.adminPayments(provider ? { provider } : {}),
  });
  const items = q.data?.items ?? [];
  return (
    <Panel
      title="Payments"
      testId="admin-payments"
      actions={
        <div className="flex items-center gap-2">
          <Select
            value={provider}
            onChange={(e) => setProvider(e.target.value)}
            aria-label="Provider"
          >
            <option value="">All providers</option>
            <option value="flutterwave">Flutterwave</option>
            <option value="ispend">iSpend</option>
            <option value="manual">Manual</option>
          </Select>
          <Button variant="primary" onClick={() => setRecording(true)}>
            Record a payment
          </Button>
        </div>
      }
    >
      {q.isError && <Alert>{errorMessage(q.error)}</Alert>}
      {items.length === 0 ? (
        <p className="text-muted">No payments yet.</p>
      ) : (
        <Table
          head={[
            "Received",
            "Organisation",
            "Amount",
            "Fee",
            "Via",
            "Reference",
            "",
          ]}
        >
          {items.map((p) => (
            <tr key={p.id} data-testid="admin-payment-row">
              <td className="px-3 py-2">{formatDate(p.received_at)}</td>
              <td className="px-3 py-2">{p.org_name ?? p.org_id}</td>
              <td className="px-3 py-2 tabular-nums">
                {naira(p.amount_minor)}
                {p.refunded_minor > 0 && (
                  <span className="text-muted">
                    {" "}
                    ({naira(p.refunded_minor)} refunded)
                  </span>
                )}
              </td>
              <td className="px-3 py-2 tabular-nums text-muted">
                {naira(p.fee_minor)}
              </td>
              <td className="px-3 py-2">
                {p.provider} · {p.channel.replace("_", " ")}
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted">
                {p.provider_ref}
              </td>
              <td className="px-3 py-2 text-right">
                {p.refunded_minor < p.amount_minor && (
                  <Button size="small" onClick={() => setRefunding(p)}>
                    Refund
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
      <RecordPayment open={recording} onClose={() => setRecording(false)} />
      <RefundPayment p={refunding} onClose={() => setRefunding(null)} />
    </Panel>
  );
}

function RecordPayment({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const orgs = useQuery({
    queryKey: ["admin-orgs"],
    queryFn: () => api.adminOrgs(),
    enabled: open,
  });
  const [org, setOrg] = useState("");
  const [amount, setAmount] = useState("");
  const [reference, setReference] = useState("");
  const [note, setNote] = useState("");
  const [invoice, setInvoice] = useState("");
  const [topup, setTopup] = useState(false);
  const [proof, setProof] = useState<File | null>(null);
  const { busy, err, run } = useAction();
  const save = () =>
    run("save", async () => {
      const kobo = parseNaira(amount);
      if (!org || !kobo || !reference)
        throw new Error(
          "Choose the organisation and enter the amount and bank reference.",
        );
      const proofKey = proof
        ? (await api.uploadProof(org, proof)).key
        : undefined;
      await api.recordPayment({
        org_id: org,
        amount_minor: kobo,
        reference,
        note: note || undefined,
        invoice_id: invoice || undefined,
        topup: topup || undefined,
        proof_key: proofKey,
      });
      await qc.invalidateQueries({ queryKey: ["admin-payments"] });
      onClose();
    });
  return (
    <SidePanel
      open={open}
      onOpenChange={(o) => !o && onClose()}
      title="Record a payment"
      description="For a transfer to PGDock's own account or a cheque. It settles the invoice given, then the oldest open ones; anything left becomes credit."
      testId="record-payment"
      footer={
        <Button variant="primary" busy={busy === "save"} onClick={save}>
          Record
        </Button>
      }
    >
      <div className="flex flex-col gap-4">
        <Field label="Organisation">
          {(id) => (
            <Select
              id={id}
              value={org}
              onChange={(e) => setOrg(e.target.value)}
            >
              <option value="">Choose…</option>
              {(orgs.data?.items ?? []).map((o) => (
                <option key={o.id} value={o.id}>
                  {o.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Amount received (₦)">
          {(id) => (
            <Input
              id={id}
              inputMode="decimal"
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
            />
          )}
        </Field>
        <Field
          label="Bank reference"
          hint="Recording the same reference twice is refused."
        >
          {(id) => (
            <Input
              id={id}
              value={reference}
              onChange={(e) => setReference(e.target.value)}
            />
          )}
        </Field>
        <Field label="Invoice ID" hint="Optional: settle this invoice first.">
          {(id) => (
            <Input
              id={id}
              value={invoice}
              onChange={(e) => setInvoice(e.target.value)}
            />
          )}
        </Field>
        <Field label="Note">
          {(id) => (
            <Input
              id={id}
              value={note}
              onChange={(e) => setNote(e.target.value)}
            />
          )}
        </Field>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={topup}
            onChange={(e) => setTopup(e.target.checked)}
          />
          Keep it all as credit (a prepaid top-up)
        </label>
        <Field label="Proof (PDF or image)">
          {(id) => (
            <input
              id={id}
              type="file"
              accept="application/pdf,image/png,image/jpeg"
              onChange={(e) => setProof(e.target.files?.[0] ?? null)}
            />
          )}
        </Field>
        {err && <Alert>{err}</Alert>}
      </div>
    </SidePanel>
  );
}

function RefundPayment({
  p,
  onClose,
}: {
  p: Payment | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [done, setDone] = useState<string | null>(null);
  const { busy, err, run } = useAction();
  const close = () => {
    setAmount("");
    setReason("");
    setDone(null);
    onClose();
  };
  const refund = () =>
    run("refund", async () => {
      const kobo = parseNaira(amount);
      if (!p || !kobo || !reason)
        throw new Error("Enter the amount and a reason.");
      const r = await api.refundPayment(p.id, kobo, reason);
      setDone(
        r.status === "succeeded"
          ? "Refunded."
          : "The refund was sent; it is posted when the provider confirms it.",
      );
      await qc.invalidateQueries({ queryKey: ["admin-payments"] });
    });
  return (
    <SidePanel
      open={!!p}
      onOpenChange={(o) => !o && close()}
      title="Refund"
      description="Refunds come out of the organisation's credit, so only unapplied money can be refunded."
      testId="refund-payment"
      footer={
        !done && (
          <Button variant="danger" busy={busy === "refund"} onClick={refund}>
            Refund
          </Button>
        )
      }
    >
      {p && (
        <div className="flex flex-col gap-4">
          <p className="text-sm">
            {naira(p.amount_minor)} from {p.org_name ?? p.org_id} on{" "}
            {formatDate(p.received_at)} ({p.provider_ref}).
          </p>
          <Field label="Amount (₦)">
            {(id) => (
              <Input
                id={id}
                inputMode="decimal"
                value={amount}
                onChange={(e) => setAmount(e.target.value)}
              />
            )}
          </Field>
          <Field label="Reason">
            {(id) => (
              <Input
                id={id}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            )}
          </Field>
          {done && <Alert tone="ok">{done}</Alert>}
          {err && <Alert>{err}</Alert>}
        </div>
      )}
    </SidePanel>
  );
}

/** WHT deducted without a credit note yet (V3 §3.6). */
export function WhtTab() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["admin-wht"], queryFn: api.outstandingWht });
  const { busy, err, run } = useAction();
  const items = q.data?.items ?? [];
  return (
    <Panel
      title="Withholding tax receivable"
      description="Invoices paid net of WHT whose credit note has not arrived. Attaching the credit note clears the receivable."
      testId="admin-wht"
      actions={
        <a href={api.whtCsvUrl()}>
          <Button>
            <Download className="h-4 w-4" /> CSV
          </Button>
        </a>
      }
    >
      {items.length === 0 ? (
        <p className="text-muted">Nothing outstanding.</p>
      ) : (
        <Table head={["Organisation", "TIN", "Invoice", "WHT", "Age", ""]}>
          {items.map((w) => (
            <tr key={w.invoice_id} data-testid="wht-row">
              <td className="px-3 py-2">{w.org_name}</td>
              <td className="px-3 py-2 font-mono text-xs">{w.tin ?? "—"}</td>
              <td className="px-3 py-2 font-mono text-xs">{w.number}</td>
              <td className="px-3 py-2 tabular-nums">{naira(w.wht_minor)}</td>
              <td className="px-3 py-2">
                <Badge
                  tone={
                    w.age_days > 90
                      ? "danger"
                      : w.age_days > 30
                        ? "warn"
                        : undefined
                  }
                >
                  {w.age_days} days
                </Badge>
              </td>
              <td className="px-3 py-2 text-right">
                <label className="cursor-pointer text-[13px] text-accent-text hover:underline">
                  {busy === w.invoice_id ? "Uploading…" : "Attach credit note"}
                  <input
                    type="file"
                    className="sr-only"
                    accept="application/pdf,image/png,image/jpeg"
                    onChange={(e) => {
                      const f = e.target.files?.[0];
                      if (f)
                        void run(w.invoice_id, async () => {
                          await api.adminUploadWht(w.invoice_id, f);
                          await qc.invalidateQueries({
                            queryKey: ["admin-wht"],
                          });
                        });
                    }}
                  />
                </label>
              </td>
            </tr>
          ))}
        </Table>
      )}
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
    </Panel>
  );
}

/** The nightly comparison with each provider's records (V3 §3.5). */
export function ReconciliationTab() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["reconciliation"],
    queryFn: api.reconciliation,
  });
  const { busy, err, run } = useAction();
  const items = q.data?.items ?? [];
  return (
    <Panel
      title="Reconciliation"
      description="Each night PGDock lists the previous day's transactions at each provider and compares them with the payments it recorded."
      testId="reconciliation"
      actions={
        <Button
          busy={busy === "run"}
          onClick={() =>
            run("run", async () => {
              await api.runReconciliation();
              await qc.invalidateQueries({ queryKey: ["reconciliation"] });
            })
          }
        >
          Run now
        </Button>
      }
    >
      {err && <Alert>{err}</Alert>}
      {items.length === 0 ? (
        <p className="text-muted">Not run yet.</p>
      ) : (
        <div className="flex flex-col gap-4">
          {items.map((r) => (
            <div
              key={r.provider}
              data-testid="reconciliation-row"
              className="text-sm"
            >
              <div className="flex items-center gap-2">
                <span className="font-medium">{r.provider}</span>
                {r.error ? (
                  <Badge tone="danger">Failed</Badge>
                ) : r.differences.length === 0 ? (
                  <Badge tone="ok">No differences</Badge>
                ) : (
                  <Badge tone="warn">{r.differences.length} differences</Badge>
                )}
                <span className="text-muted">
                  {formatDate(r.from)} – {formatDate(r.to)}: {r.matched}{" "}
                  matched, {naira(r.gross_minor)} gross, {naira(r.fees_minor)}{" "}
                  fees. Ran {formatDate(r.ran_at)}.
                </span>
              </div>
              {r.error && <p className="mt-1 text-danger">{r.error}</p>}
              {r.differences.length > 0 && (
                <Table head={["Difference", "Reference", "Provider", "PGDock"]}>
                  {r.differences.map((d) => (
                    <tr key={d.kind + d.provider_ref}>
                      <td className="px-3 py-2">
                        {d.kind.replaceAll("_", " ")}
                      </td>
                      <td className="px-3 py-2 font-mono text-xs">
                        {d.provider_ref}
                      </td>
                      <td className="px-3 py-2 tabular-nums">
                        {naira(d.provider_minor)}
                      </td>
                      <td className="px-3 py-2 tabular-nums">
                        {naira(d.pgdock_minor)}
                      </td>
                    </tr>
                  ))}
                </Table>
              )}
            </div>
          ))}
        </div>
      )}
    </Panel>
  );
}

/** Provider webhooks PGDock could not match to an organisation. */
export function EventsTab() {
  const qc = useQueryClient();
  const [outcome, setOutcome] = useState("unmatched");
  const q = useQuery({
    queryKey: ["payment-events", outcome],
    queryFn: () => api.paymentEvents(outcome || undefined),
  });
  const orgs = useQuery({
    queryKey: ["admin-orgs"],
    queryFn: () => api.adminOrgs(),
  });
  const [target, setTarget] = useState<Record<number, string>>({});
  const { busy, err, run } = useAction();
  const items = q.data?.items ?? [];
  return (
    <Panel
      title="Payment events"
      description="Every webhook from a provider, verified before it is posted. A transfer PGDock cannot match to an organisation waits here to be attributed."
      testId="payment-events"
      actions={
        <Select
          value={outcome}
          onChange={(e) => setOutcome(e.target.value)}
          aria-label="Outcome"
        >
          <option value="unmatched">Unmatched</option>
          <option value="failed">Failed</option>
          <option value="rejected">Rejected</option>
          <option value="posted">Posted</option>
          <option value="">All</option>
        </Select>
      }
    >
      {items.length === 0 ? (
        <p className="text-muted">None.</p>
      ) : (
        <Table
          head={[
            "Received",
            "Provider",
            "Kind",
            "Amount",
            "Reference",
            "Outcome",
            "",
          ]}
        >
          {items.map((ev) => (
            <tr key={ev.id} data-testid="payment-event-row">
              <td className="px-3 py-2">{formatDate(ev.received_at)}</td>
              <td className="px-3 py-2">{ev.provider}</td>
              <td className="px-3 py-2">{ev.kind}</td>
              <td className="px-3 py-2 tabular-nums">
                {ev.amount_minor != null ? naira(ev.amount_minor) : "—"}
              </td>
              <td className="px-3 py-2 font-mono text-xs text-muted">
                {ev.provider_ref}
              </td>
              <td className="px-3 py-2">
                {ev.outcome}
                {ev.error && (
                  <div className="text-[12px] text-muted">{ev.error}</div>
                )}
              </td>
              <td className="px-3 py-2">
                {ev.outcome === "unmatched" && (
                  <div className="flex justify-end gap-2">
                    <Select
                      aria-label="Organisation"
                      value={target[ev.id] ?? ""}
                      onChange={(e) =>
                        setTarget({ ...target, [ev.id]: e.target.value })
                      }
                    >
                      <option value="">Organisation…</option>
                      {(orgs.data?.items ?? []).map((o) => (
                        <option key={o.id} value={o.id}>
                          {o.name}
                        </option>
                      ))}
                    </Select>
                    <Button
                      size="small"
                      busy={busy === String(ev.id)}
                      disabled={!target[ev.id]}
                      onClick={() =>
                        run(String(ev.id), async () => {
                          await api.attributeEvent(ev.id, target[ev.id]);
                          await qc.invalidateQueries({
                            queryKey: ["payment-events"],
                          });
                        })
                      }
                    >
                      Attribute
                    </Button>
                  </div>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
      {err && (
        <div className="mt-3">
          <Alert>{err}</Alert>
        </div>
      )}
    </Panel>
  );
}

/** Holding an organisation's dunning ladder until a date. */
export function GraceControl({
  org,
  until,
}: {
  org: string;
  until?: string | null;
}) {
  const qc = useQueryClient();
  const [date, setDate] = useState(until ? until.slice(0, 10) : "");
  const { busy, err, run } = useAction();
  const save = (v: string | null) =>
    run("grace", async () => {
      await api.setGrace(
        org,
        v ? new Date(v + "T23:59:59Z").toISOString() : null,
      );
      await qc.invalidateQueries({ queryKey: ["admin", "org", org] });
    });
  return (
    <div className="flex flex-wrap items-end gap-2" data-testid="grace">
      <Field
        label="Hold dunning until"
        hint="No restriction or suspension for non-payment before this date."
      >
        {(id) => (
          <Input
            id={id}
            type="date"
            value={date}
            onChange={(e) => setDate(e.target.value)}
            className="w-44"
          />
        )}
      </Field>
      <Button busy={busy === "grace"} onClick={() => save(date || null)}>
        Save
      </Button>
      {until && (
        <Button
          onClick={() => {
            setDate("");
            void save(null);
          }}
        >
          Clear
        </Button>
      )}
      {err && <Alert>{err}</Alert>}
    </div>
  );
}
