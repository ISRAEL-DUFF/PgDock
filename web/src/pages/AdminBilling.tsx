import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { useState } from "react";
import {
  api,
  errorMessage,
  type BillingSettings,
  type PriceBook,
  type PricePreview,
} from "../api/client";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  Field,
  Input,
  Page,
  PageSkeleton,
  Panel,
  Segmented,
  Select,
  SidePanel,
  Table,
} from "../components/ui";
import { naira, parseNaira, periodLabel, periodOf } from "../lib/billing";
import { formatDate } from "../lib/format";
import { InvoiceBody, InvoiceStatus } from "./Billing";
import {
  EventsTab,
  PaymentsTab,
  ReconciliationTab,
  WhtTab,
} from "../components/AdminPayments";

type Tab =
  | "invoices"
  | "payments"
  | "wht"
  | "events"
  | "reconciliation"
  | "prices"
  | "settings"
  | "ledger";

/** The platform admin's billing: invoices and drafts, price books, tax
 * and seller settings, and the ledger check (V3 §3). */
export function AdminBillingPage() {
  const [tab, setTab] = useState<Tab>("invoices");
  return (
    <Page
      title="Billing"
      description="Each month's invoices are drafted on its last day and issued on the 1st unless held. Prices change through price books, at least 30 days ahead."
      testId="admin-billing"
      actions={
        <Segmented<Tab>
          value={tab}
          onChange={setTab}
          options={[
            { value: "invoices", label: "Invoices" },
            { value: "payments", label: "Payments" },
            { value: "wht", label: "WHT" },
            { value: "events", label: "Events" },
            { value: "reconciliation", label: "Reconciliation" },
            { value: "prices", label: "Price books" },
            { value: "settings", label: "Settings" },
            { value: "ledger", label: "Ledger" },
          ]}
        />
      }
    >
      {tab === "invoices" && <InvoicesTab />}
      {tab === "payments" && <PaymentsTab />}
      {tab === "wht" && <WhtTab />}
      {tab === "events" && <EventsTab />}
      {tab === "reconciliation" && <ReconciliationTab />}
      {tab === "prices" && <PriceBooksTab />}
      {tab === "settings" && <SettingsTab />}
      {tab === "ledger" && <LedgerTab />}
    </Page>
  );
}

function lastMonth(): string {
  const d = new Date();
  return periodOf(
    new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - 1, 1)),
  );
}

function InvoicesTab() {
  const qc = useQueryClient();
  const [period, setPeriod] = useState(periodOf(new Date()));
  const [status, setStatus] = useState("");
  const q = useQuery({
    queryKey: ["admin-invoices", period, status],
    queryFn: () =>
      api.adminInvoices({
        period: period || undefined,
        status: (status || undefined) as never,
      }),
  });
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState<string | undefined>();
  const refresh = () => qc.invalidateQueries({ queryKey: ["admin-invoices"] });
  const act = async (key: string, f: () => Promise<unknown>) => {
    setBusy(key);
    setErr(null);
    try {
      await f();
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const items = q.data?.items ?? [];
  const total = items.reduce((s, i) => s + i.total_minor, 0);

  return (
    <Panel
      title="Invoices"
      testId="admin-invoices"
      actions={
        <>
          <Input
            type="month"
            value={period}
            onChange={(e) => setPeriod(e.target.value)}
            aria-label="Usage month"
            className="w-40"
          />
          <Select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            aria-label="Status"
          >
            <option value="">Any status</option>
            <option value="draft">Drafts</option>
            <option value="issued">Issued</option>
            <option value="paid">Paid</option>
          </Select>
          <Button
            busy={busy === "draft"}
            disabled={!period}
            onClick={() => act("draft", () => api.draftInvoices(period))}
          >
            Draft {period ? periodLabel(period) : ""}
          </Button>
        </>
      }
    >
      {err && (
        <div className="mb-3">
          <Alert>{err}</Alert>
        </div>
      )}
      {items.length === 0 ? (
        <p className="text-muted">No invoices for this filter.</p>
      ) : (
        <>
          <Table head={["Organisation", "Number", "Total", "Status", "", ""]}>
            {items.map((inv) => (
              <tr key={inv.id} data-testid="admin-invoice-row">
                <td className="px-3 py-2">
                  <button
                    className="text-accent-text hover:underline"
                    onClick={() => setOpen(inv.id)}
                  >
                    {inv.org_name}
                  </button>
                </td>
                <td className="px-3 py-2 font-mono text-xs">
                  {inv.number ?? "—"}
                </td>
                <td className="px-3 py-2 tabular-nums">
                  {naira(inv.total_minor)}
                </td>
                <td className="px-3 py-2">
                  <InvoiceStatus inv={inv} />
                  {inv.hold_reason && (
                    <p className="text-xs text-muted">{inv.hold_reason}</p>
                  )}
                </td>
                <td className="px-3 py-2">
                  {inv.status === "draft" && (
                    <div className="flex gap-2">
                      <Button
                        size="small"
                        busy={busy === "hold" + inv.id}
                        onClick={() =>
                          act("hold" + inv.id, () => {
                            if (inv.held) return api.holdInvoice(inv.id, false);
                            const reason =
                              window.prompt("Why hold this invoice?") ?? "";
                            return api.holdInvoice(
                              inv.id,
                              true,
                              reason || undefined,
                            );
                          })
                        }
                      >
                        {inv.held ? "Release" : "Hold"}
                      </Button>
                      <Button
                        size="small"
                        variant="primary"
                        busy={busy === "issue" + inv.id}
                        onClick={() =>
                          act("issue" + inv.id, () => api.issueInvoice(inv.id))
                        }
                      >
                        Issue
                      </Button>
                    </div>
                  )}
                </td>
                <td className="px-3 py-2 text-right">
                  <a
                    className="inline-flex items-center gap-1 text-accent-text hover:underline"
                    href={api.adminInvoicePdfUrl(inv.id)}
                  >
                    <Download className="h-3.5 w-3.5" /> PDF
                  </a>
                </td>
              </tr>
            ))}
          </Table>
          <p className="mt-3 text-right text-[13px] text-muted">
            {items.length} invoices, {naira(total)} including VAT
          </p>
        </>
      )}
      <AdminInvoicePanel
        id={open}
        onClose={() => setOpen(undefined)}
        onChange={refresh}
      />
    </Panel>
  );
}

function AdminInvoicePanel({
  id,
  onClose,
  onChange,
}: {
  id?: string;
  onClose: () => void;
  onChange: () => void;
}) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["admin-invoice", id],
    queryFn: () => api.adminInvoice(id!),
    enabled: !!id,
  });
  const [amount, setAmount] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const credit = async () => {
    const kobo = parseNaira(amount);
    if (kobo == null || kobo <= 0) {
      setErr("The amount is in naira, before VAT.");
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      await api.creditNote(id!, kobo, reason);
      setAmount("");
      setReason("");
      await qc.invalidateQueries({ queryKey: ["admin-invoice", id] });
      onChange();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const d = q.data;
  return (
    <SidePanel
      open={!!id}
      onOpenChange={(o) => !o && onClose()}
      size="large"
      title={d?.invoice.number ?? "Draft invoice"}
      testId="admin-invoice-panel"
    >
      {d && (
        <div className="flex flex-col gap-6">
          <InvoiceBody d={d} />
          {d.invoice.status !== "draft" && (
            <Panel
              title="Credit note"
              description="Corrects an issued invoice; VAT is credited at the invoice's rate."
            >
              <div className="grid gap-3 sm:grid-cols-[10rem_1fr_auto] sm:items-end">
                <Field label="Amount (₦, before VAT)">
                  {(fid) => (
                    <Input
                      id={fid}
                      value={amount}
                      onChange={(e) => setAmount(e.target.value)}
                    />
                  )}
                </Field>
                <Field label="Reason">
                  {(fid) => (
                    <Input
                      id={fid}
                      value={reason}
                      onChange={(e) => setReason(e.target.value)}
                    />
                  )}
                </Field>
                <Button busy={busy} onClick={credit} disabled={!reason.trim()}>
                  Issue credit note
                </Button>
              </div>
              {err && (
                <div className="mt-3">
                  <Alert>{err}</Alert>
                </div>
              )}
            </Panel>
          )}
        </div>
      )}
    </SidePanel>
  );
}

function PriceBooksTab() {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["price-books"], queryFn: api.priceBooks });
  const [edit, setEdit] = useState<{
    version?: number;
    effective: string;
    json: string;
    notes: string;
  } | null>(null);
  const [preview, setPreview] = useState<{
    version: number;
    data: PricePreview;
  } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  if (!q.data) return <PageSkeleton />;
  const books = q.data.items;
  const current = books.find((b) => b.version === q.data.current_version);
  const refresh = () => qc.invalidateQueries({ queryKey: ["price-books"] });
  const act = async (key: string, f: () => Promise<void>) => {
    setBusy(key);
    setErr(null);
    setMsg(null);
    try {
      await f();
      await refresh();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const startDraft = (from: PriceBook, version?: number) => {
    const inThirty = new Date(Date.now() + 31 * 86400_000);
    const effective = version
      ? from.effective_at.slice(0, 10)
      : `${periodOf(new Date(Date.UTC(inThirty.getUTCFullYear(), inThirty.getUTCMonth() + 1, 1)))}-01`;
    setEdit({
      version,
      effective,
      json: JSON.stringify(withAddOnFields(from.prices), null, 2),
      notes: from.notes ?? "",
    });
  };
  const saveDraft = () =>
    act("save", async () => {
      let prices;
      try {
        prices = JSON.parse(edit!.json);
      } catch (e) {
        throw new Error(`The prices aren't valid JSON: ${errorMessage(e)}`);
      }
      const body = {
        effective_at: new Date(`${edit!.effective}T00:00:00Z`).toISOString(),
        prices,
        notes: edit!.notes || null,
      };
      if (edit!.version) await api.updatePriceBook(edit!.version, body);
      else await api.createPriceBook(body);
      setEdit(null);
    });

  return (
    <div className="flex flex-col gap-4">
      {err && <Alert>{err}</Alert>}
      {msg && <Alert tone="ok">{msg}</Alert>}
      <Panel
        title="Price books"
        description="Orgs move to a new book on its effective date, except grandfathered orgs and annual terms (at renewal). Billing contacts are emailed when one is published."
        actions={
          current && (
            <Button onClick={() => startDraft(current)}>
              New draft from the current book
            </Button>
          )
        }
        testId="price-books"
      >
        <Table head={["Version", "Effective", "State", "Pro", "Team", ""]}>
          {books.map((b) => (
            <tr key={b.version} data-testid="price-book-row">
              <td className="px-3 py-2">{b.version}</td>
              <td className="px-3 py-2">{formatDate(b.effective_at)}</td>
              <td className="px-3 py-2">
                {b.version === q.data.current_version ? (
                  <Badge tone="ok">Current</Badge>
                ) : b.published_at ? (
                  <Badge tone="accent">Published</Badge>
                ) : (
                  <Badge>Draft</Badge>
                )}
              </td>
              <td className="px-3 py-2 tabular-nums">
                {naira(b.prices.plans.pro?.monthly_minor ?? 0)}
              </td>
              <td className="px-3 py-2 tabular-nums">
                {naira(b.prices.plans.team?.monthly_minor ?? 0)}
              </td>
              <td className="px-3 py-2">
                <div className="flex justify-end gap-2">
                  <Button
                    size="small"
                    busy={busy === "preview" + b.version}
                    onClick={() =>
                      act("preview" + b.version, async () =>
                        setPreview({
                          version: b.version,
                          data: await api.previewPriceBook(
                            b.version,
                            lastMonth(),
                          ),
                        }),
                      )
                    }
                  >
                    Preview
                  </Button>
                  {!b.published_at && (
                    <>
                      <Button
                        size="small"
                        onClick={() => startDraft(b, b.version)}
                      >
                        Edit
                      </Button>
                      <Button
                        size="small"
                        busy={busy === "del" + b.version}
                        onClick={() =>
                          act("del" + b.version, () =>
                            api.deletePriceBook(b.version),
                          )
                        }
                      >
                        Delete
                      </Button>
                      <Button
                        size="small"
                        variant="primary"
                        busy={busy === "pub" + b.version}
                        onClick={() =>
                          act("pub" + b.version, async () => {
                            const r = await api.publishPriceBook(b.version);
                            setMsg(
                              `Published version ${b.version}; ${r.notified} organisations were emailed.`,
                            );
                          })
                        }
                      >
                        Publish
                      </Button>
                    </>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </Table>
      </Panel>
      {preview && (
        <Panel
          title={`Version ${preview.version} on ${periodLabel(preview.data.period)}'s usage`}
          description={`Before VAT: ${naira(preview.data.current_total_minor)} at each org's book now, ${naira(preview.data.projected_total_minor)} at this one.`}
          actions={<Button onClick={() => setPreview(null)}>Close</Button>}
          testId="price-preview"
        >
          <Table
            head={["Organisation", "Plan", "Now", "At this book", "Change"]}
          >
            {preview.data.items.map((i) => (
              <tr key={i.org_id}>
                <td className="px-3 py-2">{i.org_name}</td>
                <td className="px-3 py-2">{i.plan}</td>
                <td className="px-3 py-2 tabular-nums">
                  {naira(i.current_minor)}
                </td>
                <td className="px-3 py-2 tabular-nums">
                  {naira(i.projected_minor)}
                </td>
                <td className="px-3 py-2 tabular-nums">
                  {naira(i.projected_minor - i.current_minor)}
                </td>
              </tr>
            ))}
          </Table>
        </Panel>
      )}
      <SidePanel
        open={!!edit}
        onOpenChange={(o) => !o && setEdit(null)}
        size="xlarge"
        title={
          edit?.version
            ? `Edit draft version ${edit.version}`
            : "New price book draft"
        }
        description="Money is in kobo. Unit prices may have fractions of a kobo. A published book can't change."
        footer={
          <>
            <Button onClick={() => setEdit(null)}>Cancel</Button>
            <Button
              variant="primary"
              busy={busy === "save"}
              onClick={saveDraft}
            >
              Save draft
            </Button>
          </>
        }
      >
        {edit && (
          <div className="flex flex-col gap-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field
                label="Effective from (UTC)"
                hint="At least 30 days after you publish it."
              >
                {(id) => (
                  <Input
                    id={id}
                    type="date"
                    value={edit.effective}
                    onChange={(e) =>
                      setEdit({ ...edit, effective: e.target.value })
                    }
                  />
                )}
              </Field>
              <Field label="Notes">
                {(id) => (
                  <Input
                    id={id}
                    value={edit.notes}
                    onChange={(e) =>
                      setEdit({ ...edit, notes: e.target.value })
                    }
                  />
                )}
              </Field>
            </div>
            <Field label="Prices">
              {(id) => (
                <textarea
                  id={id}
                  spellCheck={false}
                  className="h-[60vh] w-full rounded-md border border-line-strong bg-surface p-3 font-mono text-xs"
                  value={edit.json}
                  onChange={(e) => setEdit({ ...edit, json: e.target.value })}
                />
              )}
            </Field>
            {err && <Alert>{err}</Alert>}
          </div>
        )}
      </SidePanel>
    </div>
  );
}

function SettingsTab() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["billing-settings"],
    queryFn: api.billingSettings,
  });
  const [s, setS] = useState<BillingSettings | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  if (!q.data) return <PageSkeleton />;
  const v = s ?? q.data;
  const seller = (k: keyof BillingSettings["seller"], value: string) =>
    setS({ ...v, seller: { ...v.seller, [k]: value } });
  const save = async () => {
    setBusy(true);
    setMsg(null);
    try {
      await api.saveBillingSettings(v);
      await qc.invalidateQueries({ queryKey: ["billing-settings"] });
      setS(null);
      setMsg({ ok: true, text: "Saved." });
    } catch (e) {
      setMsg({ ok: false, text: errorMessage(e) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <Panel
      title="Tax and invoices"
      description="Rates are configuration: confirm them with your accountant. The seller's details are frozen on each invoice when it is issued."
      testId="billing-settings"
      footer={
        <Button variant="primary" busy={busy} onClick={save}>
          Save
        </Button>
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="VAT rate" hint="A fraction: 0.075 is 7.5%.">
          {(id) => (
            <Input
              id={id}
              value={v.vat_rate}
              onChange={(e) => setS({ ...v, vat_rate: e.target.value })}
            />
          )}
        </Field>
        <Field
          label="Withholding tax rate"
          hint="Shown on invoices of orgs that deduct WHT."
        >
          {(id) => (
            <Input
              id={id}
              value={v.wht_rate}
              onChange={(e) => setS({ ...v, wht_rate: e.target.value })}
            />
          )}
        </Field>
        <Field label="Legal name">
          {(id) => (
            <Input
              id={id}
              value={v.seller.legal_name}
              onChange={(e) => seller("legal_name", e.target.value)}
            />
          )}
        </Field>
        <Field label="Billing email">
          {(id) => (
            <Input
              id={id}
              value={v.seller.email}
              onChange={(e) => seller("email", e.target.value)}
            />
          )}
        </Field>
        <Field label="TIN">
          {(id) => (
            <Input
              id={id}
              value={v.seller.tin}
              onChange={(e) => seller("tin", e.target.value)}
            />
          )}
        </Field>
        <Field label="VAT registration">
          {(id) => (
            <Input
              id={id}
              value={v.seller.vat_number}
              onChange={(e) => seller("vat_number", e.target.value)}
            />
          )}
        </Field>
        <div className="sm:col-span-2">
          <Field label="Address">
            {(id) => (
              <Input
                id={id}
                value={v.seller.address}
                onChange={(e) => seller("address", e.target.value)}
              />
            )}
          </Field>
        </div>
        <label className="flex items-center gap-2 text-sm sm:col-span-2">
          <input
            type="checkbox"
            checked={v.auto_issue}
            onChange={(e) => setS({ ...v, auto_issue: e.target.checked })}
          />
          Issue each month&apos;s drafts automatically on the 1st (turn on when
          payments are live)
        </label>
        <label className="flex items-center gap-2 text-sm sm:col-span-2">
          <input
            type="checkbox"
            checked={v.stablecoin ?? false}
            onChange={(e) => setS({ ...v, stablecoin: e.target.checked })}
          />
          Offer USDT top-ups through iSpend to prepaid organisations
        </label>
        <label className="flex items-center gap-2 text-sm sm:col-span-2">
          <input
            type="checkbox"
            checked={v.delete_for_non_payment ?? false}
            onChange={(e) =>
              setS({ ...v, delete_for_non_payment: e.target.checked })
            }
          />
          Delete dedicated projects 7 days after the deletion notice (day 40 of
          non-payment). Off, the notice is sent and nothing is deleted.
        </label>
      </div>
      {msg && (
        <div className="mt-3">
          <Alert tone={msg.ok ? "ok" : "danger"}>{msg.text}</Alert>
        </div>
      )}
    </Panel>
  );
}

function LedgerTab() {
  const q = useQuery({ queryKey: ["ledger-check"], queryFn: api.ledgerCheck });
  if (!q.data) return <PageSkeleton />;
  const c = q.data;
  return (
    <Panel
      title="Ledger"
      description="Every transaction must balance, so all debits equal all credits. Balances are debits minus credits."
      actions={
        c.balanced ? (
          <Badge tone="ok">Balanced</Badge>
        ) : (
          <Badge tone="danger">Unbalanced</Badge>
        )
      }
      testId="ledger"
    >
      <p className="mb-3 text-[13px] text-muted">
        {c.transactions} transactions; {naira(c.debits_minor)} debited and{" "}
        {naira(c.credits_minor)} credited.
      </p>
      {c.problems.length > 0 && (
        <CodeBlock code={JSON.stringify(c.problems, null, 2)} />
      )}
      <Table head={["Account", "Balance"]}>
        {c.accounts.map((a) => (
          <tr key={a.account}>
            <td className="px-3 py-2 font-mono text-xs">{a.account}</td>
            <td className="px-3 py-2 text-right tabular-nums">
              {naira(a.balance_minor)}
            </td>
          </tr>
        ))}
      </Table>
    </Panel>
  );
}

/** The add-on prices a book from before V4.1 lacks, at the default
 * book's values, so a draft shows every field to set (V4.1 §4). */
const addOnDefaults = {
  message_margin_percent: "20",
  pitr_14_hour: "685",
  pitr_30_hour: "1644",
  backup_retention_extended_hour: "205.5",
  backup_retention_long_hour: "411",
  region_premium_percent: {},
};

export function withAddOnFields(prices: PriceBook["prices"]): PriceBook["prices"] {
  return { ...prices, addons: { ...addOnDefaults, ...prices.addons } };
}

