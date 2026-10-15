import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, Landmark, Wallet as WalletIcon } from "lucide-react";
import { useState } from "react";
import {
  api,
  errorMessage,
  type BillingAccount,
  type Invoice,
  type Wallet,
  type WalletCharge,
} from "../api/client";
import { bps, channelLabel, naira, nairaRate, rate } from "../lib/billing";
import { formatDate } from "../lib/format";
import { Alert, Button, Field, Input, Panel, Table } from "./ui";

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
      <Alert tone="warn" title="The wallet couldn't pay an invoice">
        There wasn&apos;t enough in the wallet. Top it up by bank transfer to
        the account below; Xenibase pays the invoice as soon as the money
        arrives.
      </Alert>
    );
  return null;
}

/** The wallet's bank account, with what a transfer to it does. */
function BankAccount({
  org,
  wallet,
  onChange,
}: {
  org: string;
  wallet: Wallet;
  onChange: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const va = wallet.account;
  if (!va) {
    // The wallet exists but the provider didn't give its bank account (an
    // outage, say, when it was opened): ask again.
    const ask = async () => {
      setBusy(true);
      setErr(null);
      try {
        await api.virtualAccount(org);
        onChange();
      } catch (e) {
        setErr(errorMessage(e));
      } finally {
        setBusy(false);
      }
    };
    return (
      <div className="rounded-md border border-line p-3" data-testid="virtual-account-missing">
        <div className="flex items-center gap-2 font-medium">
          <Landmark className="h-4 w-4" /> Top up by bank transfer
        </div>
        <p className="mt-1 text-[12px] text-muted">
          The wallet&apos;s bank account isn&apos;t ready yet.
        </p>
        {err && <Alert tone="warn">{err}</Alert>}
        <Button className="mt-2" onClick={ask} disabled={busy}>
          {busy ? "Asking…" : "Get the bank account"}
        </Button>
      </div>
    );
  }
  return (
    <div className="rounded-md border border-line p-3" data-testid="virtual-account">
      <div className="flex items-center gap-2 font-medium">
        <Landmark className="h-4 w-4" /> Top up by bank transfer
      </div>
      <div className="mt-1 font-mono text-base" data-testid="account-number">
        {va.account_number}
      </div>
      <div className="text-muted">
        {va.bank_name} · {va.account_name}
      </div>
      <p className="mt-2 text-[12px] text-muted">
        This account is yours alone. Transfers to it top up the wallet within
        minutes.{" "}
        <strong className="font-medium text-fg">
          The bank&apos;s collection fee (1.4%, at most ₦2,000) comes off the
          amount you send.
        </strong>{" "}
        Invoices you owe are paid from the balance automatically
        {wallet.consent?.current ? "" : " once automatic debits are on"}.
      </p>
    </div>
  );
}

/** Agreeing to, or withdrawing, Xenibase debiting the wallet for invoices. */
function DebitConsentBlock({
  org,
  wallet,
  onChange,
}: {
  org: string;
  wallet: Wallet;
  onChange: () => Promise<void>;
}) {
  const [agree, setAgree] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const consent = wallet.consent;
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setErr(null);
    try {
      await f();
      setAgree(false);
      await onChange();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div
      className="flex flex-col gap-2 rounded-md border border-line p-3"
      data-testid="debit-consent"
    >
      {consent?.current ? (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span>
            Automatic debits are on since {formatDate(consent.at)}. Xenibase pays
            invoices from the wallet when they are issued and whenever it is
            topped up.
          </span>
          <Button
            size="small"
            busy={busy}
            onClick={() => {
              if (
                window.confirm(
                  "Turn off automatic debits? Invoices will then be paid by hand from this page.",
                )
              )
                void run(() => api.withdrawDebitConsent(org));
            }}
          >
            Turn off
          </Button>
        </div>
      ) : (
        <>
          <div className="font-medium">
            {consent
              ? "The debit terms changed: please agree again"
              : "Let Xenibase pay invoices from the wallet"}
          </div>
          <div
            className="max-h-40 overflow-auto whitespace-pre-line rounded-md bg-surface-2 p-2 text-[12px] text-muted"
            data-testid="debit-terms"
          >
            {wallet.terms}
          </div>
          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-1"
              checked={agree}
              onChange={(e) => setAgree(e.target.checked)}
            />
            I&apos;m authorised to agree for this organisation
          </label>
          <div>
            <Button
              variant="primary"
              busy={busy}
              disabled={!agree}
              onClick={() =>
                run(() => api.allowDebits(org, wallet.terms_version))
              }
            >
              Allow Xenibase to debit the wallet
            </Button>
          </div>
        </>
      )}
      {err && <Alert>{err}</Alert>}
    </div>
  );
}

/** Opening the organisation's wallet and its bank account. */
function WalletSetupForm({
  org,
  defaultName,
  onOpened,
}: {
  org: string;
  defaultName: string;
  onOpened: (w: Wallet) => Promise<void>;
}) {
  const contacts = useQuery({
    queryKey: ["billing-contacts", org],
    queryFn: () => api.billingContacts(org),
  });
  const [f, setF] = useState({
    business_name: defaultName,
    email: "",
    phone: "",
    bvn: "",
    tin: "",
    cac_number: "",
  });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const email = f.email || contacts.data?.items[0]?.email || "";
  const set = (k: keyof typeof f) => (e: { target: { value: string } }) =>
    setF({ ...f, [k]: e.target.value });
  const open = async () => {
    if (!f.business_name.trim() || !email.trim()) {
      setErr("The business name and an email are needed.");
      return;
    }
    if (!/^\d{11}$/.test(f.bvn)) {
      setErr("The BVN is 11 digits.");
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      await onOpened(
        await api.openWallet(org, {
          business_name: f.business_name.trim(),
          email: email.trim(),
          phone: f.phone.trim() || undefined,
          bvn: f.bvn,
          tin: f.tin.trim() || undefined,
          cac_number: f.cac_number.trim() || undefined,
        }),
      );
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="flex flex-col gap-4"
      data-testid="wallet-setup"
      onSubmit={(e) => {
        e.preventDefault();
        void open();
      }}
    >
      <p className="text-muted">
        Your wallet holds the money Xenibase bills against. You top it up by bank
        transfer to a bank account made for your organisation, and invoices are
        paid from it. Opening one takes a moment.
      </p>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Business name">
          {(id) => (
            <Input
              id={id}
              value={f.business_name}
              onChange={set("business_name")}
            />
          )}
        </Field>
        <Field label="Email">
          {(id) => (
            <Input id={id} type="email" value={email} onChange={set("email")} />
          )}
        </Field>
        <Field label="Phone" hint="Optional.">
          {(id) => (
            <Input
              id={id}
              inputMode="tel"
              value={f.phone}
              onChange={set("phone")}
            />
          )}
        </Field>
        <Field
          label="A director's BVN"
          hint="Required by the wallet provider to open the account. It is passed to them and never stored by Xenibase."
        >
          {(id) => (
            <Input
              id={id}
              inputMode="numeric"
              maxLength={11}
              autoComplete="off"
              value={f.bvn}
              onChange={set("bvn")}
            />
          )}
        </Field>
        <Field label="TIN" hint="Optional.">
          {(id) => <Input id={id} value={f.tin} onChange={set("tin")} />}
        </Field>
        <Field label="CAC number" hint="Optional.">
          {(id) => (
            <Input id={id} value={f.cac_number} onChange={set("cac_number")} />
          )}
        </Field>
      </div>
      {err && <Alert>{err}</Alert>}
      <div>
        <Button type="submit" variant="primary" busy={busy}>
          <WalletIcon className="h-4 w-4" /> Open wallet
        </Button>
      </div>
    </form>
  );
}

/** The organisation's wallet: balance, bank account, debit consent, rate. */
export function WalletPanel({
  org,
  orgName,
  a,
}: {
  org: string;
  orgName: string;
  a: BillingAccount;
}) {
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ["wallet", org], queryFn: () => api.wallet(org) });
  const refresh = async () => {
    await qc.invalidateQueries({ queryKey: ["wallet", org] });
    await qc.invalidateQueries({ queryKey: ["billing", org] });
  };
  const w = q.data;
  let body;
  if (q.isError) body = <Alert>{errorMessage(q.error)}</Alert>;
  else if (!w) body = <p className="text-muted">Loading…</p>;
  else if (!w.available)
    body = (
      <p className="text-muted" data-testid="wallet-unavailable">
        Wallets aren&apos;t configured on this platform, so invoices are paid
        outside Xenibase.
      </p>
    );
  else if (!w.wallet_id)
    body = (
      <WalletSetupForm
        org={org}
        defaultName={a.legal_name || orgName}
        onOpened={async (opened) => {
          qc.setQueryData(["wallet", org], opened);
          await refresh();
        }}
      />
    );
  else {
    const live = w.balance_minor != null && !w.balance_error;
    const r = w.rate ?? a.fx;
    body = (
      <div className="flex flex-col gap-4 text-sm">
        <div className="flex flex-wrap gap-6">
          <div>
            <div className="text-[12px] text-muted">Available now</div>
            <div className="text-lg tabular-nums" data-testid="wallet-balance">
              {naira(live ? w.balance_minor! : w.ledger_minor)}
            </div>
            {live && !!w.pending_minor && (
              <div className="text-[12px] text-muted">
                {naira(w.pending_minor)} arriving
              </div>
            )}
          </div>
          <div>
            <div className="text-[12px] text-muted">Owed on invoices</div>
            <div className="text-lg tabular-nums" data-testid="owed">
              {naira(w.owed_minor)}
            </div>
          </div>
        </div>
        {w.balance_error && (
          <Alert tone="warn">
            The live balance couldn&apos;t be read ({w.balance_error}). Showing
            Xenibase&apos;s own record of it.
          </Alert>
        )}
        <BankAccount org={org} wallet={w} onChange={refresh} />
        <DebitConsentBlock org={org} wallet={w} onChange={refresh} />
        {r && (
          <p className="text-[12px] text-muted" data-testid="fx-rate">
            {rate(r)} ({nairaRate(r.market_rate)} plus a{" "}
            {bps(r.spread_bps)} spread, priced {formatDate(r.quoted_at)}).
            Dollar prices are converted to naira when an invoice is issued.
          </p>
        )}
      </div>
    );
  }
  return (
    <Panel
      title="Wallet"
      testId="wallet"
      description="The money Xenibase bills against. Fund it by bank transfer."
    >
      {body}
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
            <td className="px-3 py-2">{channelLabel(p.channel)}</td>
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

/** Paying one invoice from the wallet, and its WHT credit note. */
export function InvoicePay({ org, inv }: { org: string; inv: Invoice }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<WalletCharge | null>(null);
  const open = inv.status === "issued" || inv.status === "partially_paid";
  const whtPending = inv.status === "paid_wht_pending";
  const refetch = async () => {
    await qc.invalidateQueries({ queryKey: ["invoice", org, inv.id] });
    for (const k of ["invoices", "billing", "wallet", "payments", "forecast"])
      await qc.invalidateQueries({ queryKey: [k, org] });
  };
  const payFromWallet = async () => {
    setBusy("wallet");
    setErr(null);
    setResult(null);
    try {
      setResult(await api.payFromWallet(org, inv.id));
      await refetch();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const upload = async (f: File | undefined) => {
    if (!f) return;
    setBusy("wht");
    setErr(null);
    try {
      await api.uploadWht(org, inv.id, f);
      await refetch();
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
          <div>
            <Button
              variant="primary"
              busy={busy === "wallet"}
              onClick={payFromWallet}
            >
              <WalletIcon className="h-4 w-4" /> Pay from wallet
            </Button>
          </div>
          <p className="text-[12px] text-muted">
            Or top up the wallet by bank transfer on the Billing page: Xenibase
            pays the invoice as soon as the money arrives.
          </p>
          {result && (
            <Alert
              tone={result.succeeded ? "ok" : "warn"}
              title={result.succeeded ? "Paid from the wallet" : "Not paid"}
            >
              <span data-testid="wallet-charge-message">
                {result.message ||
                  (result.succeeded
                    ? `${naira(result.paid_minor)} was paid.`
                    : result.unavailable
                      ? "The wallet couldn't be reached. Try again shortly."
                      : result.skipped
                        ? "Nothing was charged: the wallet has no money for it or debits are off."
                        : "The wallet couldn't pay it.")}
              </span>
            </Alert>
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
