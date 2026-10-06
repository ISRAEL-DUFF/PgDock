import { useState, type FormEvent } from "react";
import { ApiRequestError, api, errorMessage } from "../api/client";
import { Alert, Button, Dialog, Field, Input } from "./ui";

/** Runs an action, asking for the password and a code first when the
 * server wants a fresh step-up (spending money, V2 §7.2). */
export function useStepUp(
  why = "This spends money: confirm with your password and a code.",
) {
  const [pending, setPending] = useState<(() => Promise<unknown>) | null>(null);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const run = async (f: () => Promise<unknown>) => {
    try {
      await f();
    } catch (e) {
      if (e instanceof ApiRequestError && e.code === "reauth_required") {
        setPending(() => f);
        return;
      }
      throw e;
    }
  };
  const confirm = async (e: FormEvent) => {
    e.preventDefault();
    if (!pending) return;
    setBusy(true);
    setErr(null);
    try {
      await api.reauth({ password, code });
      await pending();
      setPending(null);
      setPassword("");
      setCode("");
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const dialog = (
    <Dialog
      open={!!pending}
      onOpenChange={(o) => !o && setPending(null)}
      title="Confirm it's you"
      description={why}
    >
      <form className="flex flex-col gap-3" onSubmit={confirm}>
        <Field label="Your password">
          {(id) => (
            <Input
              id={id}
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
        <Field label="Your authenticator code">
          {(id) => (
            <Input
              id={id}
              inputMode="numeric"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          )}
        </Field>
        {err && <Alert>{err}</Alert>}
        <div className="flex justify-end gap-2">
          <Button onClick={() => setPending(null)}>Cancel</Button>
          <Button
            type="submit"
            variant="primary"
            busy={busy}
            disabled={!password || code.length < 6}
          >
            Confirm
          </Button>
        </div>
      </form>
    </Dialog>
  );
  return { run, dialog };
}
