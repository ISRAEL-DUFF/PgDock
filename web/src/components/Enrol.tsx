import QRCode from "qrcode";
import { useEffect, useState, type FormEvent } from "react";
import { errorMessage } from "../api/client";
import { Alert, Button, CopyButton, CopyField, Field, Input } from "./ui";

/** Shows a TOTP secret as a QR code and asks for the first code. */
export function TotpEnrol({
  uri,
  secret,
  submitLabel,
  onCode,
}: {
  uri: string;
  secret: string;
  submitLabel: string;
  onCode: (code: string) => Promise<void>;
}) {
  const [qr, setQr] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    QRCode.toDataURL(uri, { margin: 1, width: 192 }).then(setQr, () => setQr(""));
  }, [uri]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await onCode(code);
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
      <p className="text-sm text-muted">Scan this with an authenticator app (1Password, Aegis, Google Authenticator…), then enter the code it shows. Every sign-in needs one.</p>
      <div className="flex flex-wrap items-center gap-4">
        {qr && <img src={qr} alt="TOTP QR code" className="h-48 w-48 rounded-md bg-white p-1" />}
        <div className="min-w-0 flex-1">
          <CopyField label="Or enter this key manually" value={secret} testId="totp-secret" />
        </div>
      </div>
      <Field label="Code">
        {(id) => (
          <Input id={id} inputMode="numeric" autoComplete="one-time-code" required value={code} onChange={(e) => setCode(e.target.value)} className="font-mono tracking-widest" autoFocus />
        )}
      </Field>
      {err && <Alert>{err}</Alert>}
      <Button type="submit" variant="primary" busy={busy}>
        {submitLabel}
      </Button>
    </form>
  );
}

/** Recovery codes, shown once after enrolling (V2 §3.1). */
export function RecoveryCodes({ codes, onDone, doneLabel = "Continue" }: { codes: string[]; onDone: () => void; doneLabel?: string }) {
  const [saved, setSaved] = useState(false);
  const text = codes.join("\n");
  return (
    <div className="flex flex-col gap-4" data-testid="recovery-codes">
      <h1 className="text-lg font-semibold">Save your recovery codes</h1>
      <p className="text-sm text-muted">
        Each code signs you in once if you lose your authenticator. They are shown only now: keep them in a password manager or on paper.
      </p>
      <ul className="grid grid-cols-2 gap-1 rounded-md border border-line bg-surface-2 p-3 font-mono text-sm">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>
      <div className="flex flex-wrap gap-2">
        <CopyButton value={text} label="Copy codes" />
        <Button
          onClick={() => {
            const a = document.createElement("a");
            a.href = URL.createObjectURL(new Blob([text + "\n"], { type: "text/plain" }));
            a.download = "pgdock-recovery-codes.txt";
            a.click();
            URL.revokeObjectURL(a.href);
          }}
        >
          Download
        </Button>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={saved} onChange={(e) => setSaved(e.target.checked)} data-testid="codes-saved" />
        I've saved these codes
      </label>
      <Button variant="primary" disabled={!saved} onClick={onDone}>
        {doneLabel}
      </Button>
    </div>
  );
}
