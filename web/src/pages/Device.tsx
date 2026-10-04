import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useState, type FormEvent } from "react";
import { ApiRequestError, api, errorMessage, type TokenScope } from "../api/client";
import { ScopePicker } from "../components/Tokens";
import { Alert, Button, Panel, Field, Input, PageHeading, Select, PanelSkeleton } from "../components/ui";
import { formatDate } from "../lib/format";

/** Approve a CLI device login (V2 §7.1): pick the organisation and scopes. */
export function DevicePage() {
  const { code } = useSearch({ from: "/app/device" });
  const navigate = useNavigate();
  const [entered, setEntered] = useState(code ?? "");
  if (!code) {
    return (
      <>
        <PageHeading title="Log in the pgdock CLI" />
        <Panel className="max-w-md">
          <form
            className="flex flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              void navigate({ to: "/device", search: { code: entered.trim().toUpperCase() } });
            }}
          >
            <Field label="Code shown in your terminal">
              {(id) => (
                <Input id={id} value={entered} onChange={(e) => setEntered(e.target.value)} placeholder="BCDF-GHJK" className="font-mono uppercase" autoFocus />
              )}
            </Field>
            <Button type="submit" variant="primary" className="self-start" disabled={!entered.trim()}>
              Continue
            </Button>
          </form>
        </Panel>
      </>
    );
  }
  return <Approve code={code} />;
}

function Approve({ code }: { code: string }) {
  const req = useQuery({ queryKey: ["device", code], queryFn: () => api.deviceRequest(code), retry: false });
  const orgs = useQuery({ queryKey: ["orgs"], queryFn: api.orgs });
  const [org, setOrg] = useState("");
  const [scopes, setScopes] = useState<TokenScope[] | null>(null);
  const [days, setDays] = useState(90);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [done, setDone] = useState<"approved" | "denied" | null>(null);
  useEffect(() => {
    if (!org && orgs.data?.items.length) setOrg(orgs.data.items[0].id);
  }, [org, orgs.data]);
  if (done) {
    return (
      <>
        <PageHeading title="Log in the pgdock CLI" />
        <Panel className="max-w-lg">
          <p className="text-sm" data-testid={`device-${done}`}>
            {done === "approved" ? "Approved. Return to your terminal: the CLI is logged in." : "Denied. The CLI was not logged in."}
          </p>
        </Panel>
      </>
    );
  }
  if (req.isPending) return <PanelSkeleton rows={2} />;
  if (req.isError) {
    const gone = req.error instanceof ApiRequestError && req.error.status === 404;
    return (
      <>
        <PageHeading title="Log in the pgdock CLI" />
        <Alert>
          {gone ? `No pending login with the code ${code}. It may have expired (codes last 10 minutes): run pgdock login again.` : errorMessage(req.error)}
        </Alert>
      </>
    );
  }
  const r = req.data;
  const chosen = scopes ?? r.scopes;
  const decide = async (approve: boolean, e?: FormEvent) => {
    e?.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await api.approveDevice(
        approve ? { user_code: r.user_code, approve: true, org_id: org, scopes: chosen, expires_in_days: days } : { user_code: r.user_code, approve: false },
      );
      setDone(approve ? "approved" : "denied");
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeading
        title="Log in the pgdock CLI"
        description={`Code ${r.user_code}, requested by “${r.client_name || "pgdock CLI"}”. Expires ${formatDate(r.expires_at)}.`}
      />
      <Panel className="max-w-lg">
        <form className="flex flex-col gap-4" onSubmit={(e) => void decide(true, e)}>
          <Alert tone="warn">Only approve a code you started yourself, in your own terminal, just now.</Alert>
          <Field label="Organisation" hint="The CLI will act in this organisation only.">
            {(id) => (
              <Select id={id} value={org} onChange={(e) => setOrg(e.target.value)} data-testid="device-org">
                {orgs.data?.items.map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.name}
                    {o.personal ? " (personal)" : ""}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <ScopePicker value={chosen} onChange={setScopes} allowed={r.scopes} />
          <Field label="Expires after">
            {(id) => (
              <Select id={id} value={days} onChange={(e) => setDays(Number(e.target.value))}>
                {[7, 30, 90, 180, 365].map((d) => (
                  <option key={d} value={d}>
                    {d} days
                  </option>
                ))}
              </Select>
            )}
          </Field>
          {err && <Alert>{err}</Alert>}
          <div className="flex justify-end gap-2">
            <Button onClick={() => void decide(false)} disabled={busy} data-testid="device-deny">
              Deny
            </Button>
            <Button type="submit" variant="primary" busy={busy} disabled={!org} data-testid="device-approve">
              Approve
            </Button>
          </div>
        </form>
      </Panel>
    </>
  );
}
