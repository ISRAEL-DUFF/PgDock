import { useNavigate, useSearch } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { api, errorMessage } from "../api/client";
import { AuthShell } from "../components/Layout";
import { Alert, Button, Field, Input } from "../components/ui";
import { setSession } from "../lib/session";

export function LoginPage() {
  const { next } = useSearch({ from: "/login" });
  const navigate = useNavigate();
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [challenge, setChallenge] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submitPassword = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const c = await api.login({ email, password });
      setChallenge(c.challenge_id);
      setPassword("");
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const submitCode = async (e: FormEvent) => {
    e.preventDefault();
    if (!challenge) return;
    setBusy(true);
    setErr(null);
    try {
      setSession(qc, await api.totp({ challenge_id: challenge, code }));
      await navigate({ to: next ?? "/projects" });
    } catch (e) {
      setErr(errorMessage(e));
      setCode("");
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthShell>
      {!challenge ? (
        <form className="flex flex-col gap-4" onSubmit={submitPassword}>
          <h1 className="text-lg font-semibold">Sign in</h1>
          <Field label="Email">
            {(id) => <Input id={id} type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />}
          </Field>
          <Field label="Password">
            {(id) => (
              <Input id={id} type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
            )}
          </Field>
          {err && <Alert>{err}</Alert>}
          <Button type="submit" variant="primary" busy={busy}>
            Continue
          </Button>
        </form>
      ) : (
        <form className="flex flex-col gap-4" onSubmit={submitCode}>
          <h1 className="text-lg font-semibold">Two-factor code</h1>
          <p className="text-sm text-muted">Enter the 6-digit code from your authenticator app.</p>
          <Field label="Code">
            {(id) => (
              <Input
                id={id}
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9 ]{6,7}"
                required
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoFocus
                className="font-mono tracking-widest"
              />
            )}
          </Field>
          {err && <Alert>{err}</Alert>}
          <Button type="submit" variant="primary" busy={busy}>
            Sign in
          </Button>
          <Button variant="ghost" onClick={() => setChallenge(null)}>
            Start over
          </Button>
        </form>
      )}
    </AuthShell>
  );
}
