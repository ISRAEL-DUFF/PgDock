import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type Prices } from "../api/client";
import {
  Alert,
  Field,
  Input,
  Page,
  PageSkeleton,
  Panel,
  Select,
  Table,
} from "../components/ui";
import { naira } from "../lib/billing";
import { formatDate } from "../lib/format";

/** Hours in a billing month, as estimates use (730). */
const MONTH_HOURS = 730;

/** "shared_storage_gb_hours" → "Shared storage GB-hours". */
function metricLabel(m: string): string {
  const s = m
    .replaceAll("_", " ")
    .replace(/\b(gb|mb)\b/g, (u) => u.toUpperCase())
    .replace(/ hours$/, "-hours");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function num(v: string | undefined): number {
  const n = Number(v ?? "0");
  return Number.isFinite(n) ? n : 0;
}

/** Plans side by side: fees, allowances and overage prices. */
function PlanTable({ prices }: { prices: Prices }) {
  const plans = Object.entries(prices.plans).sort(
    ([, a], [, b]) => a.monthly_minor - b.monthly_minor,
  );
  const metrics = [
    ...new Set(
      plans.flatMap(([, p]) => [
        ...Object.keys(p.included),
        ...Object.keys(p.unit),
      ]),
    ),
  ].sort();
  return (
    <Table head={["", ...plans.map(([, p]) => p.name)]}>
      <tr>
        <td className="px-3 py-2 text-muted">Monthly</td>
        {plans.map(([k, p]) => (
          <td
            key={k}
            className="px-3 py-2 font-medium tabular-nums"
            data-testid={`plan-monthly-${k}`}
          >
            {p.monthly_minor > 0 ? naira(p.monthly_minor) : "Free"}
          </td>
        ))}
      </tr>
      <tr>
        <td className="px-3 py-2 text-muted">A year in advance</td>
        {plans.map(([k, p]) => (
          <td key={k} className="px-3 py-2 tabular-nums">
            {p.annual_minor > 0 ? naira(p.annual_minor) : "—"}
          </td>
        ))}
      </tr>
      {metrics.map((m) => (
        <tr key={m}>
          <td className="px-3 py-2 text-muted">{metricLabel(m)}</td>
          {plans.map(([k, p]) => {
            const inc = p.included[m];
            const unit = p.unit[m];
            return (
              <td key={k} className="px-3 py-2 text-[13px]">
                {inc ? (
                  <>{num(inc).toLocaleString("en-NG")} included</>
                ) : unit ? (
                  "—"
                ) : (
                  "Hard limit"
                )}
                {unit && (
                  <span className="block text-muted">
                    then {naira(num(unit))} each
                  </span>
                )}
              </td>
            );
          })}
        </tr>
      ))}
    </Table>
  );
}

/** What a plan plus a dedicated instance comes to a month. */
function Calculator({ prices, vat }: { prices: Prices; vat: number }) {
  const keys = Object.keys(prices.plans).sort(
    (a, b) => prices.plans[a].monthly_minor - prices.plans[b].monthly_minor,
  );
  const [plan, setPlan] = useState(
    keys.find((k) => prices.plans[k].monthly_minor > 0) ?? keys[0],
  );
  const [cpus, setCpus] = useState("0");
  const [ram, setRam] = useState("0");
  const [disk, setDisk] = useState("0");
  const [ha, setHa] = useState(false);
  const d = prices.dedicated;
  const fee = prices.plans[plan]?.monthly_minor ?? 0;
  const instance =
    MONTH_HOURS *
    (num(cpus) * num(d.vcpu_hour) +
      num(ram) * num(d.ram_gb_hour) +
      num(disk) * num(d.disk_gb_hour));
  const standby = ha
    ? instance * (1 + num(prices.addons.ha_premium_percent) / 100)
    : 0;
  const subtotal = fee + instance + standby;
  return (
    <Panel
      title="Estimate"
      description="A plan, plus a dedicated instance running all month. Usage above the allowances is extra."
      testId="pricing-calculator"
    >
      <div className="grid gap-4 sm:grid-cols-5">
        <Field label="Plan">
          {(id) => (
            <Select
              id={id}
              value={plan}
              onChange={(e) => setPlan(e.target.value)}
            >
              {keys.map((k) => (
                <option key={k} value={k}>
                  {prices.plans[k].name}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Dedicated vCPUs">
          {(id) => (
            <Input
              id={id}
              inputMode="decimal"
              value={cpus}
              onChange={(e) => setCpus(e.target.value)}
            />
          )}
        </Field>
        <Field label="RAM (GB)">
          {(id) => (
            <Input
              id={id}
              inputMode="decimal"
              value={ram}
              onChange={(e) => setRam(e.target.value)}
            />
          )}
        </Field>
        <Field label="Disk (GB)">
          {(id) => (
            <Input
              id={id}
              inputMode="decimal"
              value={disk}
              onChange={(e) => setDisk(e.target.value)}
            />
          )}
        </Field>
        <label className="flex items-end gap-2 pb-2 text-sm">
          <input
            type="checkbox"
            checked={ha}
            onChange={(e) => setHa(e.target.checked)}
          />
          High availability
        </label>
      </div>
      <dl className="mt-4 grid max-w-md grid-cols-2 gap-y-1 text-sm">
        <dt className="text-muted">Plan</dt>
        <dd className="text-right tabular-nums">{naira(fee)}</dd>
        <dt className="text-muted">Dedicated instance</dt>
        <dd className="text-right tabular-nums">{naira(instance)}</dd>
        {ha && (
          <>
            <dt className="text-muted">Standby (HA)</dt>
            <dd className="text-right tabular-nums">{naira(standby)}</dd>
          </>
        )}
        <dt className="text-muted">VAT ({(vat * 100).toFixed(1)}%)</dt>
        <dd className="text-right tabular-nums">{naira(subtotal * vat)}</dd>
        <dt className="font-medium">A month</dt>
        <dd
          className="text-right font-medium tabular-nums"
          data-testid="estimate-total"
        >
          {naira(subtotal * (1 + vat))}
        </dd>
      </dl>
    </Panel>
  );
}

/** The prices in effect, plan by plan, and an estimate (V3 §11). */
export function PricingPage() {
  const q = useQuery({ queryKey: ["pricing"], queryFn: api.pricing });
  if (q.isError) return <Alert>{errorMessage(q.error)}</Alert>;
  if (!q.data) return <PageSkeleton />;
  const p = q.data;
  const vat = num(p.vat_rate);
  const d = p.prices.dedicated;
  return (
    <Page
      title="Pricing"
      description={`Prices in naira, before VAT, from price book ${p.version} (in effect since ${formatDate(p.effective_at)}). Free projects pause after a week without connections.`}
      testId="pricing"
    >
      {p.next && (
        <Alert tone="warn" title="New prices are coming">
          Price book {p.next.version} takes effect on{" "}
          {formatDate(p.next.effective_at)}.
        </Alert>
      )}
      <Panel title="Plans">
        <PlanTable prices={p.prices} />
      </Panel>
      <Panel
        title="Dedicated instances"
        description="Billed by the hour for what each instance has."
      >
        <Table head={["Resource", "An hour", `A month (${MONTH_HOURS} h)`]}>
          {[
            ["A vCPU", d.vcpu_hour],
            ["A GB of RAM", d.ram_gb_hour],
            ["A GB of disk", d.disk_gb_hour],
          ].map(([label, v]) => (
            <tr key={label}>
              <td className="px-3 py-2">{label}</td>
              <td className="px-3 py-2 tabular-nums">{naira(num(v))}</td>
              <td className="px-3 py-2 tabular-nums">
                {naira(num(v) * MONTH_HOURS)}
              </td>
            </tr>
          ))}
          <tr>
            <td className="px-3 py-2">High availability</td>
            <td className="px-3 py-2" colSpan={2}>
              The standby at the same rates, plus{" "}
              {num(p.prices.addons.ha_premium_percent)}%
            </td>
          </tr>
          <tr>
            <td className="px-3 py-2">Synchronous replication</td>
            <td className="px-3 py-2 tabular-nums">
              {naira(num(p.prices.addons.sync_replication_hour))}
            </td>
            <td className="px-3 py-2 tabular-nums">
              {naira(num(p.prices.addons.sync_replication_hour) * MONTH_HOURS)}
            </td>
          </tr>
          {(
            [
              ["14-day point-in-time recovery", p.prices.addons.pitr_14_hour],
              ["30-day point-in-time recovery", p.prices.addons.pitr_30_hour],
              [
                "Extended backup retention (30 daily, 12 weekly)",
                p.prices.addons.backup_retention_extended_hour,
              ],
              [
                "Long backup retention (30 daily, 52 weekly)",
                p.prices.addons.backup_retention_long_hour,
              ],
            ] as [string, string | undefined][]
          )
            .filter(([, v]) => num(v) > 0)
            .map(([label, v]) => (
              <tr key={label}>
                <td className="px-3 py-2">{label}</td>
                <td className="px-3 py-2 tabular-nums">{naira(num(v))}</td>
                <td className="px-3 py-2 tabular-nums">
                  {naira(num(v) * MONTH_HOURS)}
                </td>
              </tr>
            ))}
          {Object.entries(p.prices.addons.region_premium_percent ?? {})
            .filter(([, v]) => num(v) > 0)
            .map(([region, v]) => (
              <tr key={region}>
                <td className="px-3 py-2">Region premium: {region}</td>
                <td className="px-3 py-2" colSpan={2}>
                  {num(v)}% on dedicated, HA and read replica prices there
                </td>
              </tr>
            ))}
        </Table>
      </Panel>
      <Calculator prices={p.prices} vat={vat} />
    </Page>
  );
}
