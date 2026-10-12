import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CreditCard, Download, Landmark, Wallet } from "lucide-react";
import { useState } from "react";
import {
  api,
  errorMessage,
  type BillingAccount,
  type CheckoutRequest,
  type Invoice,
  type VirtualAccount,
} from "../api/client";
import { naira, parseNaira } from "../lib/billing";
import { formatDate } from "../lib/format";
import { Alert, Badge, Button, Field, Input, Panel, Table } from "./ui";

/** Sends the browser to the provider's hosted page. */
async function goToCheckout(org: string, b: CheckoutRequest) {
  const c = await api.checkout(org, b);
  window.location.assign(c.checkout_url);
}

/** What the org owes or is restricted for (V3 §3.8), with a way to pay. */
export function StandingBanner({ a }: { a: BillingAccount }) {
  const owed = a.owed_minor ?? 0;
  if (a.dunning_state === "suspended")
    return (
      <Alert title="Suspended for non-payment">
        The organisation&apos;s databases are offline until {naira(owed)} is
        paid; its data is kept.
        {a.deletion_scheduled_at && (
          <>
            {" "}
            Its dedicated projects are due for deletion on{" "}
            {formatDate(a.deletion_scheduled_at)}.
          </>
        )}
      </Alert>
    );
  if (a.dunning_state === "restricted")
    return (
      <Alert tone="warn" title="Payment overdue">
        {naira(owed)} is overdue since {formatDate(a.overdue_since)}. Creating
        projects, branches and dedicated instances is blocked until it is paid.
      </Alert>
    );
  if (a.dunning_state === "overdue")
    return (
      <Alert tone="warn" title="Payment overdue">
        {naira(owed)} is overdue since {formatDate(a.overdue_since)}.
      </Alert>
    );
  if (a.card_failing_since)
    return (
      <Alert tone="warn" title="The saved card was declined">
        PGDock will try again; update the card or pay another way below.
      </Alert>
    );
  if (a.mode === "prepaid" && a.zero_balance_at)
    return (
      <Alert tone="warn" title="The prepaid balance has run out">
        Top up within 3 days of {formatDate(a.zero_balance_at)} to avoid
        restrictions.
      </Alert>
    );
  return null;
}

/** The balance, the transfer account, and top-ups. */
export function BalancePanel({ org, a }: { org: string; a: BillingAccount }) {
  const qc = useQueryClient();
  const [va, setVA] = useState<VirtualAccount | null>(null);
  const [amount, setAmount] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [below, setBelow] = useState(
    a.auto_topup ? String(a.auto_topup.below_minor / 100) : "",
  );
  const [topAmt, setTopAmt] = useState(
    a.auto_topup ? String(a.auto_topup.amount_minor / 100) : "",
  );
  const ch = a.channels ?? {
    card: false,
    wallet: false,
    transfer: false,
    stablecoin: false,
  };
  const act = async (key: string, f: () => Promise<unknown>) => {
    setBusy(key);
    setErr(null);
    try {
      await f();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const topup = (channel: CheckoutRequest["channel"]) =>
    act(channel, async () => {
      const kobo = parseNaira(amount);
      if (!kobo) throw new Error("Enter an amount in naira.");
      await goToCheckout(org, {
        channel,
        purpose: "topup",
        amount_minor: kobo,
      });
    });
  const prepaid = a.mode === "prepaid";
  return (
    <Panel
      title={prepaid ? "Prepaid balance" : "Balance"}
      testId="balance"
      description={
        prepaid
          ? "Usage is deducted daily from the balance."
          : "Invoices are due within the payment terms; anything paid beyond them is kept as credit."
      }
    >
      <div className="flex flex-col gap-4 text-sm">
        <div className="flex flex-wrap gap-6">
          <div>
            <div className="text-[12px] text-muted">Credit</div>
            <div className="text-lg tabular-nums" data-testid="credit">
              {naira(a.credit_minor ?? 0)}
            </div>
          </div>
          {!prepaid && (
            <div>
              <div className="text-[12px] text-muted">Owed</div>
              <div className="text-lg tabular-nums" data-testid="owed">
                {naira(a.owed_minor ?? 0)}
              </div>
            </div>
          )}
        </div>
        {ch.transfer && (
          <div>
            {va ? (
              <div
                className="rounded-md border border-line p-3"
                data-testid="virtual-account"
              >
                <div className="flex items-center gap-2 font-medium">
                  <Landmark className="h-4 w-4" /> Pay by bank transfer
                </div>
                <div className="mt-1 font-mono text-base">
                  {va.account_number}
                </div>
                <div className="text-muted">
                  {va.bank_name} · {va.account_name}
                </div>
                <p className="mt-1 text-[12px] text-muted">
                  This account is yours alone: transfers settle the oldest
                  invoice{prepaid ? " or top up the balance" : ""}, usually
                  within minutes.
                </p>
              </div>
            ) : (
              <Button
                busy={busy === "va"}
                onClick={() =>
                  act("va", async () => setVA(await api.virtualAccount(org)))
                }
              >
                <Landmark className="h-4 w-4" /> Show my bank transfer account
              </Button>
            )}
          </div>
        )}
        {(ch.card || ch.wallet) && (
          <div className="flex flex-wrap items-end gap-2">
            <Field label="Top up (₦)">
              {(id) => (
                <Input
                  id={id}
                  inputMode="decimal"
                  value={amount}
                  onChange={(e) => setAmount(e.target.value)}
                  placeholder="20,000"
                  className="w-36"
                />
              )}
            </Field>
            {ch.card && (
              <Button busy={busy === "card"} onClick={() => topup("card")}>
                <CreditCard className="h-4 w-4" /> By card
              </Button>
            )}
            {ch.wallet && (
              <Button busy={busy === "wallet"} onClick={() => topup("wallet")}>
                <Wallet className="h-4 w-4" /> With iSpend
              </Button>
            )}
            {ch.stablecoin && prepaid && (
              <Button
                busy={busy === "stablecoin"}
                onClick={() => topup("stablecoin")}
              >
                USDT (iSpend)
              </Button>
            )}
          </div>
        )}
        {prepaid && (
          <div
            className="flex flex-wrap items-end gap-2"
            data-testid="auto-topup"
          >
            <Field label="Top up automatically below (₦)">
              {(id) => (
                <Input
                  id={id}
                  inputMode="decimal"
                  value={below}
                  onChange={(e) => setBelow(e.target.value)}
                  className="w-36"
                />
              )}
            </Field>
            <Field label="by (₦)">
              {(id) => (
                <Input
                  id={id}
                  inputMode="decimal"
                  value={topAmt}
                  onChange={(e) => setTopAmt(e.target.value)}
                  className="w-36"
                />
              )}
            </Field>
            <Button
              busy={busy === "auto"}
              onClick={() =>
                act("auto", async () => {
                  const b = parseNaira(below);
                  const t = parseNaira(topAmt);
                  if (b == null || !t) await api.clearAutoTopup(org);
                  else
                    await api.setAutoTopup(org, {
                      below_minor: b,
                      amount_minor: t,
                    });
                  await qc.invalidateQueries({ queryKey: ["billing", org] });
                })
              }
            >
              Save
            </Button>
          </div>
        )}
        {err && <Alert>{err}</Alert>}
      </div>
    </Panel>
  );
}

/** Saved cards and mandates. */
export function MethodsPanel({ org, a }: { org: string; a: BillingAccount }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["payment-methods", org],
    queryFn: () => api.paymentMethods(org),
  });
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const run = async (key: string, f: () => Promise<unknown>) => {
    setBusy(key);
    setErr(null);
    try {
      await f();
      await qc.invalidateQueries({ queryKey: ["payment-methods", org] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const items = q.data?.items ?? [];
  if (!a.channels?.card && !a.channels?.wallet && items.length === 0)
    return null;
  return (
    <Panel
      title="Payment methods"
      description="The default method is charged when an invoice is issued (and for automatic top-ups)."
      testId="payment-methods"
      actions={
        a.channels?.card && (
          <Button
            busy={busy === "add"}
            onClick={() =>
              run("add", () =>
                goToCheckout(org, { channel: "card", purpose: "card_setup" }),
              )
            }
          >
            <CreditCard className="h-4 w-4" /> Add a card
          </Button>
        )
      }
    >
      {items.length === 0 ? (
        <p className="text-muted">
          No saved methods. Adding a card charges ₦100, kept as credit. An
          iSpend mandate is set up when you pay an invoice with iSpend.
        </p>
      ) : (
        <Table head={["Method", "Details", ""]}>
          {items.map((m) => (
            <tr key={m.id} data-testid="payment-method">
              <td className="px-3 py-2">
                {m.kind === "card"
                  ? `${m.brand ?? "Card"} ending ${m.last4}`
                  : "iSpend wallet mandate"}{" "}
                {m.is_default && <Badge tone="accent">Default</Badge>}
              </td>
              <td className="px-3 py-2 text-muted">
                {m.kind === "card"
                  ? `Expires ${String(m.exp_month).padStart(2, "0")}/${m.exp_year}`
                  : `Up to ${naira(m.limit_minor ?? 0)} a month`}
              </td>
              <td className="px-3 py-2">
                <div className="flex justify-end gap-2">
                  {!m.is_default && (
                    <Button
                      size="small"
                      busy={busy === "d" + m.id}
                      onClick={() =>
                        run("d" + m.id, () =>
                          api.defaultPaymentMethod(org, m.id),
                        )
                      }
                    >
                      Make default
                    </Button>
                  )}
                  <Button
                    size="small"
                    busy={busy === "r" + m.id}
                    onClick={() =>
                      run("r" + m.id, () => api.removePaymentMethod(org, m.id))
                    }
                  >
                    Remove
                  </Button>
                </div>
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

/** Payments received, with receipts. */
export function PaymentsPanel({ org }: { org: string }) {
  const q = useQuery({
    queryKey: ["payments", org],
    queryFn: () => api.payments(org),
  });
  const items = q.data?.items ?? [];
  if (items.length === 0) return null;
  return (
    <Panel title="Payments" testId="payments">
      <Table head={["Received", "Amount", "Method", "Reference", ""]}>
        {items.map((p) => (
          <tr key={p.id} data-testid="payment-row">
            <td className="px-3 py-2">{formatDate(p.received_at)}</td>
            <td className="px-3 py-2 tabular-nums">{naira(p.amount_minor)}</td>
            <td className="px-3 py-2">{p.channel.replace("_", " ")}</td>
            <td className="px-3 py-2 font-mono text-xs text-muted">
              {p.provider_ref}
            </td>
            <td className="px-3 py-2 text-right">
              <a
                className="inline-flex items-center gap-1 text-accent-text hover:underline"
                href={api.receiptUrl(org, p.id)}
              >
                <Download className="h-3.5 w-3.5" /> Receipt
              </a>
            </td>
          </tr>
        ))}
      </Table>
    </Panel>
  );
}

/** Paying one invoice, and its WHT credit note. */
export function InvoicePay({
  org,
  inv,
  a,
}: {
  org: string;
  inv: Invoice;
  a?: BillingAccount;
}) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [mandate, setMandate] = useState("");
  const open = inv.status === "issued" || inv.status === "partially_paid";
  const whtPending = inv.status === "paid_wht_pending";
  const pay = (channel: "card" | "wallet") => async () => {
    setBusy(channel);
    setErr(null);
    try {
      await goToCheckout(org, {
        channel,
        purpose: "invoice",
        invoice_id: inv.id,
        mandate_limit_minor:
          channel === "wallet" ? (parseNaira(mandate) ?? undefined) : undefined,
      });
    } catch (e) {
      setErr(errorMessage(e));
      setBusy(null);
    }
  };
  const upload = async (f: File | undefined) => {
    if (!f) return;
    setBusy("wht");
    setErr(null);
    try {
      await api.uploadWht(org, inv.id, f);
      await qc.invalidateQueries({ queryKey: ["invoice", org, inv.id] });
      await qc.invalidateQueries({ queryKey: ["invoices", org] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  if (!open && !whtPending) return null;
  return (
    <div
      className="flex flex-col gap-3 rounded-md border border-line p-3 text-sm"
      data-testid="invoice-pay"
    >
      {open && (
        <>
          <div className="font-medium">Pay this invoice</div>
          <div className="flex flex-wrap items-end gap-2">
            {a?.channels?.card && (
              <Button
                variant="primary"
                busy={busy === "card"}
                onClick={pay("card")}
              >
                <CreditCard className="h-4 w-4" /> Pay by card
              </Button>
            )}
            {a?.channels?.wallet && (
              <>
                <Button busy={busy === "wallet"} onClick={pay("wallet")}>
                  <Wallet className="h-4 w-4" /> Pay with iSpend
                </Button>
                <Field label="and allow automatic payments up to (₦/month)">
                  {(id) => (
                    <Input
                      id={id}
                      inputMode="decimal"
                      value={mandate}
                      onChange={(e) => setMandate(e.target.value)}
                      placeholder="optional"
                      className="w-36"
                    />
                  )}
                </Field>
              </>
            )}
          </div>
          {a?.channels?.transfer && (
            <p className="text-[12px] text-muted">
              Or transfer to your bank account on the Billing page.
            </p>
          )}
        </>
      )}
      {whtPending && (
        <label className="flex flex-col gap-1">
          <span className="font-medium">Upload the WHT credit note</span>
          <span className="text-[12px] text-muted">
            {naira(inv.wht_deducted_minor || inv.wht_expected_minor)} of
            withholding tax was deducted. PDF, PNG or JPEG, up to 10 MB.
          </span>
          <input
            type="file"
            accept="application/pdf,image/png,image/jpeg"
            disabled={busy === "wht"}
            onChange={(e) => upload(e.target.files?.[0])}
          />
        </label>
      )}
      {err && <Alert>{err}</Alert>}
    </div>
  );
}
