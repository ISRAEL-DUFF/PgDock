import { useEffect, useRef } from "react";

type TurnstileAPI = {
  render: (
    el: HTMLElement,
    opts: {
      sitekey: string;
      callback: (token: string) => void;
      "expired-callback": () => void;
      "error-callback": () => void;
    },
  ) => string;
  remove: (id: string) => void;
};

declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

const SCRIPT =
  "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
let loading: Promise<void> | null = null;

function load(): Promise<void> {
  if (window.turnstile) return Promise.resolve();
  loading ??= new Promise<void>((resolve, reject) => {
    const s = document.createElement("script");
    s.src = SCRIPT;
    s.async = true;
    s.onload = () => resolve();
    s.onerror = () => {
      loading = null;
      reject(new Error("the anti-bot check couldn't load"));
    };
    document.head.appendChild(s);
  });
  return loading;
}

/** Cloudflare Turnstile's challenge (V3 §7.4); onToken gets the token, or
 * "" when it expires or fails. */
export function Turnstile({
  siteKey,
  onToken,
}: {
  siteKey: string;
  onToken: (token: string) => void;
}) {
  const el = useRef<HTMLDivElement>(null);
  const cb = useRef(onToken);
  cb.current = onToken;
  useEffect(() => {
    let id: string | undefined;
    let gone = false;
    load()
      .then(() => {
        if (gone || !el.current || !window.turnstile) return;
        id = window.turnstile.render(el.current, {
          sitekey: siteKey,
          callback: (t) => cb.current(t),
          "expired-callback": () => cb.current(""),
          "error-callback": () => cb.current(""),
        });
      })
      .catch(() => cb.current(""));
    return () => {
      gone = true;
      if (id && window.turnstile) window.turnstile.remove(id);
    };
  }, [siteKey]);
  return <div ref={el} data-testid="turnstile" />;
}
