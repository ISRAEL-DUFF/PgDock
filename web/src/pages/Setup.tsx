import { useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import QRCode from "qrcode";
import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage, type DnsCheck, type SetupEnrollment } from "../api/client";
import { AuthShell } from "../components/Layout";
import { Alert, Button, CopyField, Field, Input } from "../components/ui";
import { sessionQuery, setSession } from "../lib/session";

type Step = "account" | "totp" | "host" | "done";

/** First-run wizard (spec §8.2; storage and backups arrive in M3). */
export function SetupPage() {
  const { data: session } = useQuery(sessionQuery);
  const [step, setStep] = useState<Step>("account");
  const [enrollment, setEnrollment] = useState<SetupEnrollment | null>(null);
  const navigate = useNavigate();

  // Setup already done (and this tab isn't mid-wizard): go to sign-in.
  useEffect(() => {
    if (session && !session.setup_required && step === "account") void navigate({ to: "/login" });
  }, [session, step, navigate]);

  const steps: { id: Step; label: string }[] = [
    { id: "account", label: "Owner account" },
    { id: "totp", label: "Two-factor" },
    { id: "host", label: "Database hostname" },
    { id: "done", label: "Done" },
  ];
  const current = steps.findIndex((s) => s.id === step);

  return (
    <AuthShell wide>
      <ol className="mb-6 flex gap-2 text-xs">
        {steps.map((s, i) => (
          <li key={s.id} className={i === current ? "font-semibold text-fg" : i < current ? "text-ok" : "text-muted"}>
            {i + 1}. {s.label}
            {i < steps.length - 1 && <span className="ml-2 text-muted">›</span>}
          </li>
        ))}
      </ol>
      {step === "account" && (
        <AccountStep
          onDone={(e) => {
            setEnrollment(e);
            setStep("totp");
          }}
        />
      )}
      {step === "totp" && enrollment && <TotpStep enrollment={enrollment} onDone={() => setStep("host")} />}
      {step === "host" && <HostStep onDone={() => setStep("done")} />}
      {step === "done" && (
        <div className="flex flex-col gap-4">
          <h1 className="text-lg font-semibold">PGDock is ready</h1>
          <p className="text-sm text-muted">Backups and S3 storage are configured in a later release; for now, create your first database.</p>
          <Button variant="primary" onClick={() => navigate({ to: "/projects/new" })}>
            Create your first project
          </Button>
        </div>
      )}
    </AuthShell>
  );
}

function AccountStep({ onDone }: { onDone: (e: SetupEnrollment) => void }) {
  const [setupCode, setSetupCode] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const mismatch = confirm !== "" && confirm !== password;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      onDone(await api.setupBegin({ setup_code: setupCode.trim(), email, password }));
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="flex flex-col gap-4" onSubmit={submit}>
      <h1 className="text-lg font-semibold">Create the owner account</h1>
      <Field label="Setup code" hint={<>Printed in the server log: <code className="font-mono">docker compose logs pgdock-server</code></>}>
        {(id) => <Input id={id} required value={setupCode} onChange={(e) => setSetupCode(e.target.value)} className="font-mono" autoComplete="off" autoFocus />}
      </Field>
      <Field label="Email">
        {(id) => <Input id={id} type="email" required autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} />}
      </Field>
      <Field label="Password" hint="At least 12 characters.">
        {(id) => <Input id={id} type="password" required minLength={12} autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />}
      </Field>
      <Field label="Confirm password" error={mismatch ? "Passwords do not match." : null}>
        {(id) => <Input id={id} type="password" required autoComplete="new-password" value={confirm} onChange={(e) => setConfirm(e.target.value)} />}
      </Field>
      {err && <Alert>{err}</Alert>}
      <Button type="submit" variant="primary" busy={busy} disabled={mismatch || password.length < 12}>
        Continue
      </Button>
    </form>
  );
}

function TotpStep({ enrollment, onDone }: { enrollment: SetupEnrollment; onDone: () => void }) {
  const qc = useQueryClient();
  const [qr, setQr] = useState<string>("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    QRCode.toDataURL(enrollment.totp_uri, { margin: 1, width: 192 }).then(setQr, () => setQr(""));
  }, [enrollment.totp_uri]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      setSession(qc, await api.setupComplete({ enrollment_token: enrollment.enrollment_token, code }));
      onDone();
    } catch (e) {
      setErr(errorMessage(e));
      setCode("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="flex flex-col gap-4" onSubmit={submit}>
      <h1 className="text-lg font-semibold">Set up two-factor authentication</h1>
      <p className="text-sm text-muted">Scan this with an authenticator app (1Password, Aegis, Google Authenticator…), then enter the code it shows.</p>
      <div className="flex flex-wrap items-center gap-4">
        {qr && <img src={qr} alt="TOTP QR code" className="h-48 w-48 rounded-md bg-white p-1" />}
        <div className="min-w-0 flex-1">
          <CopyField label="Or enter this key manually" value={enrollment.totp_secret} testId="totp-secret" />
        </div>
      </div>
      <Field label="Code">
        {(id) => (
          <Input id={id} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} className="font-mono tracking-widest" autoFocus />
        )}
      </Field>
      {err && <Alert>{err}</Alert>}
      <Button type="submit" variant="primary" busy={busy}>
        Verify and create account
      </Button>
    </form>
  );
}

export function HostStep({ onDone, submitLabel = "Save and continue" }: { onDone: () => void; submitLabel?: string }) {
  const settings = useQuery({ queryKey: ["settings", "general"], queryFn: api.generalSettings });
  const qc = useQueryClient();
  const [host, setHost] = useState<string | null>(null);
  const [check, setCheck] = useState<DnsCheck | null>(null);
  const [busy, setBusy] = useState<"check" | "save" | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const value = host ?? settings.data?.db_host ?? "";

  const runCheck = async () => {
    setBusy("check");
    setErr(null);
    try {
      setCheck(await api.checkDbHost(value));
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setBusy("save");
    setErr(null);
    try {
      await api.setDbHost(value);
      await qc.invalidateQueries({ queryKey: ["settings"] });
      onDone();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <form className="flex flex-col gap-4" onSubmit={save}>
      <h1 className="text-lg font-semibold">Database hostname</h1>
      <p className="text-sm text-muted">
        Clients connect to this name, e.g. <code className="font-mono">db.example.com</code>. Point its DNS at this server; PGDock then gets a
        TLS certificate for it.
      </p>
      <Field label="Hostname">
        {(id) => (
          <div className="flex gap-2">
            <Input
              id={id}
              required
              value={value}
              onChange={(e) => {
                setHost(e.target.value);
                setCheck(null);
              }}
              className="font-mono"
              placeholder="db.example.com"
            />
            <Button onClick={runCheck} busy={busy === "check"} disabled={!value}>
              Check DNS
            </Button>
          </div>
        )}
      </Field>
      {check && (
        <Alert tone={check.points_here ? "ok" : "warn"} title={check.points_here ? "DNS points at this server" : "DNS does not point here yet"}>
          <p className="font-mono text-xs">
            {check.host} → {check.addresses.length ? check.addresses.join(", ") : check.error ?? "no addresses"}
          </p>
          {!check.points_here && <p className="mt-1 text-xs">This server: {check.server_addresses.join(", ") || "unknown"}. You can continue and fix DNS later.</p>}
        </Alert>
      )}
      {err && <Alert>{err}</Alert>}
      <Button type="submit" variant="primary" busy={busy === "save"} disabled={!value}>
        {submitLabel}
      </Button>
    </form>
  );
}
