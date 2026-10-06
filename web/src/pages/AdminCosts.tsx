import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { useState, type FormEvent } from "react";
import { api, errorMessage, type CostSettings } from "../api/client";
import {
  Alert,
  Badge,
  Button,
  Field,
  Input,
  Page,
  PageSkeleton,
  Panel,
  SidePanel,
  Stat,
  Table,
} from "../components/ui";
import { naira, periodLabel, periodOf } from "../lib/billing";

const CATEGORY_LABEL: Record<string, string> = {
  shared: "Shared nodes",
  dedicated: "Dedicated hosts",
  ha: "HA standbys",
  backup: "Backup storage",
  egress: "Data transfer",
  floating_ip: "Floating IPs",
  overhead: "Fixed overheads",
  idle: "Idle capacity",
};

function native(m: Record<string, number>): string {
  return Object.entries(m)
    .map(([c, v]) =>
      c === "NGN"
        ? naira(v)
        : `${c} ${(v / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`,
    )
    .join(" + ");
}

function pctText(v?: number | null): string {
  return v == null ? "—" : `${v.toFixed(1)}%`;
}

/** Platform → Costs & margins (V3 §5.4, §7.2): infrastructure cost by
 * region and tier, gross margin per plan and organisation, the cost of the
 * Free tier, unit costs for pricing, and the FX view. */
export function AdminCostsPage() {
  const qc = useQueryClient();
  const [month, setMonth] = useState(periodOf(new Date()));
  const q = useQuery({
    queryKey: ["admin", "costs", month],
    queryFn: () => api.costs(month),
  });
  const rates = useQuery({ queryKey: ["admin", "fx"], queryFn: api.fxRates });
  const [rateCur, setRateCur] = useState("EUR");
  const [rate, setRate] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const act = async (key: string, f: () => Promise<unknown>) => {
    setBusy(key);
    setErr(null);
    try {
      await f();
      await qc.invalidateQueries({ queryKey: ["admin"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };
  const months = Array.from({ length: 12 }, (_, i) => {
    const d = new Date();
    return periodOf(
      new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth() - i, 1)),
    );
  });
  if (q.isPending) return <PageSkeleton />;
  if (q.error) return <Alert>{errorMessage(q.error)}</Alert>;
  const m = q.data;
  const recompute = () => {
    const [y, mo] = month.split("-").map(Number);
    const first = `${month}-01`;
    const today = new Date();
    const last = new Date(Date.UTC(y, mo, 0));
    const end = last > today ? today : last;
    return api.attributeCosts(first, end.toISOString().slice(0, 10));
  };
  return (
    <Page
      title="Costs & margins"
      description={`Each day's infrastructure cost, attributed to the organisations that used it (shared nodes by storage and connection-hours, dedicated hosts by vCPU), in its billing currency and in naira. ${m.days} day${m.days === 1 ? "" : "s"} of ${periodLabel(m.month)} attributed.`}
      testId="admin-costs"
      actions={
        <>
          <select
            aria-label="Month"
            className="h-[30px] rounded-md border border-line-strong bg-surface-2 px-2 text-[13px]"
            value={month}
            onChange={(e) => setMonth(e.target.value)}
          >
            {months.map((p) => (
              <option key={p} value={p}>
                {periodLabel(p)}
              </option>
            ))}
          </select>
          <Button
            busy={busy === "attr"}
            onClick={() => void act("attr", recompute)}
          >
            Recompute
          </Button>
          <Button onClick={() => setSettingsOpen(true)}>Cost settings</Button>
          <a
            className="inline-flex h-[30px] items-center gap-1.5 rounded-md border border-line-strong bg-surface-2 px-2.5 text-[13px] hover:bg-surface-3"
            href={api.costsCsvUrl(month)}
            download
            data-testid="costs-csv"
          >
            <Download className="h-3.5 w-3.5" /> CSV
          </a>
        </>
      }
    >
      {err && <Alert>{err}</Alert>}
      {m.missing_rates && m.missing_rates.length > 0 && (
        <Alert tone="warn" title="Exchange rates missing">
          Costs in {m.missing_rates.join(", ")} are left out of the naira totals
          until you record a rate below.
        </Alert>
      )}
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <Stat label="Revenue" value={naira(m.revenue_minor)} />
        <Stat
          label="Infrastructure cost"
          value={naira(m.cost_minor)}
          hint={`${naira(m.unallocated_minor)} unallocated`}
          testId="costs-total"
        />
        <Stat
          label="Gross margin"
          value={naira(m.margin_minor)}
          hint={pctText(m.margin_pct)}
          testId="costs-margin"
        />
        <Stat
          label="FX erosion"
          value={naira(m.fx_erosion_minor)}
          hint={`at today's rate vs the rate when incurred; Free tier cost ${naira(m.free_tier_cost_minor)}`}
          testId="costs-fx"
        />
      </div>

      <Panel title="Cost by region and tier">
        <Table
          head={[
            "Region",
            "Category",
            "Billed in",
            "At today's rate",
            "At the rate then",
          ]}
        >
          {m.categories.map((c) => (
            <tr key={c.region + c.category} data-testid={`cost-${c.category}`}>
              <td className="px-3 py-2 text-xs">{c.region}</td>
              <td className="px-3 py-2">
                {CATEGORY_LABEL[c.category] ?? c.category}
              </td>
              <td className="px-3 py-2 text-xs">{native(c.native_minor)}</td>
              <td className="px-3 py-2">{naira(c.ngn_minor)}</td>
              <td className="px-3 py-2 text-xs text-muted">
                {naira(c.ngn_booked_minor)}
              </td>
            </tr>
          ))}
        </Table>
      </Panel>

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="Margin by plan">
          <Table head={["Plan", "Orgs", "Revenue", "Cost", "Margin"]}>
            {m.plans.map((p) => (
              <tr key={p.plan} data-testid={`plan-margin-${p.plan}`}>
                <td className="px-3 py-2 capitalize">{p.plan}</td>
                <td className="px-3 py-2">{p.orgs}</td>
                <td className="px-3 py-2">{naira(p.revenue_minor)}</td>
                <td className="px-3 py-2">{naira(p.cost_minor)}</td>
                <td className="px-3 py-2">
                  {naira(p.margin_minor)}{" "}
                  <span className="text-xs text-muted">
                    {pctText(p.margin_pct)}
                  </span>
                </td>
              </tr>
            ))}
          </Table>
        </Panel>
        <Panel
          title="Unit costs"
          description="What a unit costs this month: the input to repricing (unit price = unit cost × (1 + target margin) × FX buffer)."
        >
          <Table head={["Unit", "Used", "Cost per unit"]}>
            {m.units.map((u) => (
              <tr key={u.unit}>
                <td className="px-3 py-2">{u.unit}</td>
                <td className="px-3 py-2 text-xs">{u.quantity}</td>
                <td className="px-3 py-2">
                  {naira(Math.round(u.per_unit_ngn_minor))}
                  <span className="text-xs text-muted">
                    {" "}
                    ({u.currency} {(u.per_unit_minor / 100).toFixed(4)})
                  </span>
                </td>
              </tr>
            ))}
          </Table>
        </Panel>
      </div>

      <Panel title="Margin by organisation" description="Lowest margin first.">
        <Table head={["Organisation", "Plan", "Revenue", "Cost", "Margin"]}>
          {m.orgs.map((o) => (
            <tr key={o.org_id} data-testid={`org-margin-${o.name}`}>
              <td className="px-3 py-2">{o.name}</td>
              <td className="px-3 py-2 text-xs capitalize">{o.plan}</td>
              <td className="px-3 py-2">{naira(o.revenue_minor)}</td>
              <td className="px-3 py-2">
                {naira(o.cost_minor)}
                <div className="text-xs text-muted">
                  {native(o.cost_native_minor)}
                </div>
              </td>
              <td className="px-3 py-2">
                <span className={o.margin_minor < 0 ? "text-danger-text" : ""}>
                  {naira(o.margin_minor)}
                </span>{" "}
                <span className="text-xs text-muted">
                  {pctText(o.margin_pct)}
                </span>
              </td>
            </tr>
          ))}
        </Table>
      </Panel>

      <Panel
        title="Exchange rates"
        description="Naira per unit. Costs are shown at the latest rate, and at the rate in effect each day for the erosion figure."
        testId="fx-rates"
      >
        <div className="mb-3 flex flex-wrap gap-2">
          {(rates.data?.current ?? []).map((r) => (
            <Badge key={r.id} tone="muted">
              1 {r.currency} = ₦{r.ngn_per_unit.toLocaleString()}
            </Badge>
          ))}
          {rates.data && rates.data.current.length === 0 && (
            <span className="text-sm text-muted">No rates yet.</span>
          )}
        </div>
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void act("rate", async () => {
              await api.setFxRate(rateCur, Number(rate));
              setRate("");
            });
          }}
        >
          <Field label="Currency">
            {(id) => (
              <Input
                id={id}
                className="w-20"
                value={rateCur}
                maxLength={3}
                onChange={(e) => setRateCur(e.target.value.toUpperCase())}
              />
            )}
          </Field>
          <Field label="Naira per unit">
            {(id) => (
              <Input
                id={id}
                inputMode="decimal"
                value={rate}
                onChange={(e) => setRate(e.target.value)}
                data-testid="fx-rate"
              />
            )}
          </Field>
          <Button
            type="submit"
            busy={busy === "rate"}
            disabled={!(Number(rate) > 0)}
            data-testid="fx-save"
          >
            Record rate
          </Button>
        </form>
      </Panel>
      <CostSettingsPanel
        open={settingsOpen}
        onClose={() => setSettingsOpen(false)}
      />
    </Page>
  );
}

function CostSettingsPanel({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["admin", "cost-settings"],
    queryFn: api.costSettings,
    enabled: open,
  });
  const [s, setS] = useState<CostSettings | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const cur = s ?? q.data ?? null;
  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!cur) return;
    setBusy(true);
    setErr(null);
    try {
      await api.saveCostSettings(cur);
      await qc.invalidateQueries({ queryKey: ["admin"] });
      setS(null);
      onClose();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const set = (patch: Partial<CostSettings>) =>
    cur && setS({ ...cur, ...patch });
  return (
    <SidePanel
      open={open}
      onOpenChange={(o) => !o && (setS(null), onClose())}
      title="Cost settings"
      description="Costs outside the provider's server catalog. Node costs are on Platform → Capacity."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            type="submit"
            form="cost-settings-form"
            variant="primary"
            busy={busy}
          >
            Save
          </Button>
        </>
      }
    >
      {!cur ? (
        <PageSkeleton />
      ) : (
        <form
          id="cost-settings-form"
          className="flex flex-col gap-4"
          onSubmit={save}
        >
          <Field label="Currency of these prices">
            {(id) => (
              <Input
                id={id}
                value={cur.currency}
                maxLength={3}
                onChange={(e) =>
                  set({ currency: e.target.value.toUpperCase() })
                }
              />
            )}
          </Field>
          <Field label="Object storage, per GB-month (cents)">
            {(id) => (
              <Input
                id={id}
                inputMode="decimal"
                value={String(cur.object_storage_gb_month_minor)}
                onChange={(e) =>
                  set({ object_storage_gb_month_minor: Number(e.target.value) })
                }
              />
            )}
          </Field>
          <Field label="Data transfer, per GB (cents)">
            {(id) => (
              <Input
                id={id}
                inputMode="decimal"
                value={String(cur.egress_gb_minor)}
                onChange={(e) =>
                  set({ egress_gb_minor: Number(e.target.value) })
                }
              />
            )}
          </Field>
          <div className="grid grid-cols-2 gap-2">
            <Field label="Floating IPs">
              {(id) => (
                <Input
                  id={id}
                  inputMode="numeric"
                  value={String(cur.floating_ips)}
                  onChange={(e) =>
                    set({ floating_ips: Number(e.target.value) })
                  }
                />
              )}
            </Field>
            <Field label="Each, per month (cents)">
              {(id) => (
                <Input
                  id={id}
                  inputMode="numeric"
                  value={String(cur.floating_ip_monthly_minor)}
                  onChange={(e) =>
                    set({ floating_ip_monthly_minor: Number(e.target.value) })
                  }
                />
              )}
            </Field>
          </div>
          <h3 className="text-[14px]">Fixed overheads</h3>
          {cur.overheads.map((o, i) => (
            <div
              key={i}
              className="grid grid-cols-[1fr_7rem_5rem_auto] items-end gap-2"
            >
              <Field label="Name">
                {(id) => (
                  <Input
                    id={id}
                    value={o.name}
                    onChange={(e) =>
                      set({
                        overheads: cur.overheads.map((x, j) =>
                          j === i ? { ...x, name: e.target.value } : x,
                        ),
                      })
                    }
                  />
                )}
              </Field>
              <Field label="Monthly (minor)">
                {(id) => (
                  <Input
                    id={id}
                    inputMode="numeric"
                    value={String(o.monthly_minor)}
                    onChange={(e) =>
                      set({
                        overheads: cur.overheads.map((x, j) =>
                          j === i
                            ? { ...x, monthly_minor: Number(e.target.value) }
                            : x,
                        ),
                      })
                    }
                  />
                )}
              </Field>
              <Field label="Currency">
                {(id) => (
                  <Input
                    id={id}
                    maxLength={3}
                    value={o.currency}
                    onChange={(e) =>
                      set({
                        overheads: cur.overheads.map((x, j) =>
                          j === i
                            ? { ...x, currency: e.target.value.toUpperCase() }
                            : x,
                        ),
                      })
                    }
                  />
                )}
              </Field>
              <Button
                size="tiny"
                onClick={() =>
                  set({ overheads: cur.overheads.filter((_, j) => j !== i) })
                }
              >
                Remove
              </Button>
            </div>
          ))}
          <Button
            className="self-start"
            onClick={() =>
              set({
                overheads: [
                  ...cur.overheads,
                  { name: "", monthly_minor: 0, currency: cur.currency },
                ],
              })
            }
          >
            Add an overhead
          </Button>
          {err && <Alert>{err}</Alert>}
        </form>
      )}
    </SidePanel>
  );
}
