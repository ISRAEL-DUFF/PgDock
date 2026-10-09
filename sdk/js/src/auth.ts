import { PgdockError, type Result, fail, ok } from "./errors.js";
import { buildURL, json, send, type Transport } from "./http.js";

/** A signed-in user, as the API returns it. */
export interface User {
  id: string;
  email?: string | null;
  phone?: string | null;
  email_confirmed_at?: string | null;
  phone_confirmed_at?: string | null;
  is_anonymous?: boolean;
  app_metadata: Record<string, unknown>;
  user_metadata: Record<string, unknown>;
  identities?: { id: string; provider: string; provider_id: string; identity_data?: Record<string, unknown> }[];
  factors?: { id: string; factor_type: string; friendly_name?: string; status: string }[];
  created_at: string;
  updated_at?: string;
  last_sign_in_at?: string | null;
  [k: string]: unknown;
}

/** Tokens and the user. */
export interface Session {
  access_token: string;
  refresh_token: string;
  token_type: string;
  expires_in: number;
  /** Seconds since the epoch. */
  expires_at: number;
  user: User;
}

export type AuthEvent = "INITIAL_SESSION" | "SIGNED_IN" | "SIGNED_OUT" | "TOKEN_REFRESHED" | "USER_UPDATED" | "MFA_VERIFIED";

/**
 * Where the session is kept between launches: localStorage in browsers by
 * default, memory elsewhere. React Native passes AsyncStorage; Node
 * servers usually keep the default.
 */
export interface SessionStorage {
  getItem(key: string): string | null | Promise<string | null>;
  setItem(key: string, value: string): void | Promise<void>;
  removeItem(key: string): void | Promise<void>;
}

export function memoryStorage(): SessionStorage {
  const m = new Map<string, string>();
  return {
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => void m.set(k, v),
    removeItem: (k) => void m.delete(k),
  };
}

function defaultStorage(): SessionStorage {
  try {
    const ls = (globalThis as { localStorage?: Storage }).localStorage;
    if (ls) {
      const probe = "__pgdock_probe";
      ls.setItem(probe, "1");
      ls.removeItem(probe);
      return ls;
    }
  } catch {
    // private mode, or no DOM
  }
  return memoryStorage();
}

export interface AuthOptions {
  storage?: SessionStorage;
  /** The storage key (default pgdock.auth.<host>). */
  storageKey?: string;
  /** Keep and restore the session (default true). */
  persistSession?: boolean;
  /** Refresh the access token before it expires (default true). */
  autoRefresh?: boolean;
  /** Seconds before expiry to refresh (default 60). */
  refreshMargin?: number;
  /** Read a session from the URL after an email link or OAuth (browsers; default true). */
  detectSessionInUrl?: boolean;
}

export type OAuthProvider = "google" | "apple" | "github" | "facebook" | "microsoft";

type Credentials =
  | { email: string; password: string; phone?: never }
  | { phone: string; password: string; email?: never };

type Listener = (event: AuthEvent, session: Session | null) => void;

function b64url(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function randomVerifier(): string {
  const bytes = new Uint8Array(48);
  globalThis.crypto.getRandomValues(bytes);
  return b64url(bytes);
}

async function challengeOf(verifier: string): Promise<{ challenge: string; method: "s256" | "plain" }> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) return { challenge: verifier, method: "plain" };
  const digest = await subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  return { challenge: b64url(new Uint8Array(digest)), method: "s256" };
}

/** client.auth: sign-up and sign-in, the session, and the user. */
export class AuthClient {
  private session: Session | null = null;
  private listeners = new Set<Listener>();
  private refreshing: Promise<Result<Session>> | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private storage: SessionStorage;
  private storageKey: string;
  private opts: Required<Omit<AuthOptions, "storage" | "storageKey">>;
  /** Resolves once the stored session (or the URL's) is loaded. */
  readonly ready: Promise<void>;

  constructor(
    private t: Omit<Transport, "token">,
    opts: AuthOptions = {},
  ) {
    this.storage = opts.storage ?? defaultStorage();
    this.storageKey = opts.storageKey ?? "pgdock.auth." + new URL(t.url).host;
    this.opts = {
      persistSession: opts.persistSession ?? true,
      autoRefresh: opts.autoRefresh ?? true,
      refreshMargin: opts.refreshMargin ?? 60,
      detectSessionInUrl: opts.detectSessionInUrl ?? true,
    };
    this.ready = this.restore();
  }

  /** The transport services use: it carries the user's token, refreshed first. */
  transport(): Transport {
    return { ...this.t, token: () => this.accessToken() };
  }

  private bare(): Transport {
    return { ...this.t, token: async () => null };
  }

  // ---- Session ------------------------------------------------------------------

  private async restore(): Promise<void> {
    if (this.opts.detectSessionInUrl && (await this.fromURL())) return;
    if (this.opts.persistSession) {
      try {
        const raw = await this.storage.getItem(this.storageKey);
        if (raw) this.session = JSON.parse(raw) as Session;
      } catch {
        this.session = null;
      }
    }
    if (this.session && this.expiresSoon(this.session)) {
      await this.refreshSession();
    } else {
      this.schedule();
    }
    this.emit("INITIAL_SESSION");
  }

  /** A session in the URL's fragment, or a PKCE code in its query. */
  private async fromURL(): Promise<boolean> {
    const loc = (globalThis as { location?: Location }).location;
    if (!loc) return false;
    const frag = new URLSearchParams(loc.hash.replace(/^#/, ""));
    const code = new URLSearchParams(loc.search).get("code");
    if (frag.get("access_token") && frag.get("refresh_token")) {
      const r = await this.setSession({ access_token: frag.get("access_token")!, refresh_token: frag.get("refresh_token")! });
      this.cleanURL(loc, ["access_token", "refresh_token", "expires_in", "expires_at", "token_type", "type"], []);
      return !r.error;
    }
    if (code && (await this.storage.getItem(this.storageKey + ".verifier"))) {
      const r = await this.exchangeCodeForSession(code);
      this.cleanURL(loc, [], ["code"]);
      return !r.error;
    }
    return false;
  }

  private cleanURL(loc: Location, fragKeys: string[], queryKeys: string[]) {
    const h = (globalThis as { history?: History }).history;
    if (!h) return;
    const u = new URL(loc.href);
    for (const k of queryKeys) u.searchParams.delete(k);
    const frag = new URLSearchParams(u.hash.replace(/^#/, ""));
    for (const k of fragKeys) frag.delete(k);
    u.hash = frag.toString();
    h.replaceState(h.state, "", u.toString());
  }

  private expiresSoon(s: Session): boolean {
    return s.expires_at - Date.now() / 1000 < this.opts.refreshMargin;
  }

  private schedule() {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
    if (!this.session || !this.opts.autoRefresh) return;
    const ms = Math.max(0, (this.session.expires_at - this.opts.refreshMargin) * 1000 - Date.now());
    this.timer = setTimeout(() => void this.refreshSession(), Math.min(ms, 2 ** 31 - 1));
    (this.timer as { unref?: () => void }).unref?.();
  }

  private async save(s: Session | null, event: AuthEvent) {
    this.session = s;
    if (this.opts.persistSession) {
      try {
        if (s) await this.storage.setItem(this.storageKey, JSON.stringify(s));
        else await this.storage.removeItem(this.storageKey);
      } catch {
        // storage full or gone: the session lives in memory
      }
    }
    this.schedule();
    this.emit(event);
  }

  private emit(event: AuthEvent) {
    for (const l of this.listeners) {
      try {
        l(event, this.session);
      } catch {
        // a listener's error is its own
      }
    }
  }

  /** Called on every sign-in, refresh, update and sign-out; returns the unsubscribe. */
  onAuthStateChange(listener: Listener): () => void {
    this.listeners.add(listener);
    void this.ready.then(() => {
      if (this.listeners.has(listener)) listener("INITIAL_SESSION", this.session);
    });
    return () => this.listeners.delete(listener);
  }

  /** The current session (after any stored one is loaded). */
  async getSession(): Promise<Session | null> {
    await this.ready;
    if (this.session && this.expiresSoon(this.session)) await this.refreshSession();
    return this.session;
  }

  private async accessToken(): Promise<string | null> {
    const s = await this.getSession();
    return s?.access_token ?? null;
  }

  /**
   * New tokens from the refresh token. One refresh runs at a time: a
   * refresh token works once, and presenting it twice ends the session.
   */
  refreshSession(): Promise<Result<Session>> {
    if (this.refreshing) return this.refreshing;
    const s = this.session;
    if (!s) return Promise.resolve(fail(new PgdockError(401, "no_session", "not signed in")));
    this.refreshing = (async () => {
      try {
        const r = await json<Session>(this.bare(), "/auth/v1/token", {
          query: { grant_type: "refresh_token" },
          body: { refresh_token: s.refresh_token },
        });
        if (r.error) {
          // A network failure keeps the session for the next try; a refusal ends it.
          if (r.error.status >= 400 && r.error.status < 500) await this.save(null, "SIGNED_OUT");
          else if (this.opts.autoRefresh) this.retryLater();
          return r;
        }
        await this.save(r.data, "TOKEN_REFRESHED");
        return r;
      } finally {
        this.refreshing = null;
      }
    })();
    return this.refreshing;
  }

  private retryLater() {
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => void this.refreshSession(), 10_000);
    (this.timer as { unref?: () => void }).unref?.();
  }

  /** Adopt tokens from elsewhere (a deep link, a server). */
  async setSession(tokens: { access_token: string; refresh_token: string }): Promise<Result<Session>> {
    const u = await json<User>(this.bare(), "/auth/v1/user", { token: tokens.access_token });
    if (u.error) {
      this.session = { access_token: "", refresh_token: tokens.refresh_token, token_type: "bearer", expires_in: 0, expires_at: 0, user: {} as User };
      return this.refreshSession();
    }
    const exp = jwtExp(tokens.access_token) ?? Math.floor(Date.now() / 1000) + 3600;
    const s: Session = { ...tokens, token_type: "bearer", expires_in: exp - Math.floor(Date.now() / 1000), expires_at: exp, user: u.data };
    await this.save(s, "SIGNED_IN");
    return ok(s);
  }

  private async signedIn(r: Result<Session | { confirmation_sent: boolean }>): Promise<Result<{ session: Session | null; user: User | null }>> {
    if (r.error) return r;
    if ("access_token" in r.data) {
      await this.save(r.data, "SIGNED_IN");
      return ok({ session: r.data, user: r.data.user });
    }
    return ok({ session: null, user: null });
  }

  // ---- Signing up and in --------------------------------------------------------

  /**
   * An email or phone account with a password (data goes in the user's
   * metadata). With confirmation on, session is null until the code or
   * link is used.
   */
  async signUp(p: Credentials & { data?: Record<string, unknown>; redirectTo?: string; captchaToken?: string; channel?: "sms" | "whatsapp" }) {
    const r = await json<Session | { confirmation_sent: boolean }>(this.bare(), "/auth/v1/signup", {
      body: { email: p.email, phone: p.phone, password: p.password, data: p.data, redirect_to: p.redirectTo, captcha_token: p.captchaToken, channel: p.channel },
    });
    return this.signedIn(r);
  }

  /** A new anonymous user (when the project allows them). */
  async signInAnonymously(p: { data?: Record<string, unknown>; captchaToken?: string } = {}) {
    const r = await json<Session>(this.bare(), "/auth/v1/signup", { body: { data: p.data, captcha_token: p.captchaToken } });
    return this.signedIn(r);
  }

  async signInWithPassword(p: Credentials & { captchaToken?: string }) {
    const r = await json<Session>(this.bare(), "/auth/v1/signin/password", {
      body: { email: p.email, phone: p.phone, password: p.password, captcha_token: p.captchaToken },
    });
    return this.signedIn(r);
  }

  /** A code (and, by email, a magic link) to sign in with; verifyOtp finishes. */
  async signInWithOtp(
    p: ({ email: string; phone?: never } | { phone: string; email?: never; channel?: "sms" | "whatsapp" }) & {
      createUser?: boolean;
      redirectTo?: string;
      data?: Record<string, unknown>;
      captchaToken?: string;
    },
  ): Promise<Result<{ sent: true }>> {
    const r = await json<unknown>(this.bare(), "/auth/v1/signin/otp", {
      body: {
        email: p.email,
        phone: p.phone,
        channel: "channel" in p ? p.channel : undefined,
        create_user: p.createUser,
        redirect_to: p.redirectTo,
        data: p.data,
        captcha_token: p.captchaToken,
      },
    });
    return r.error ? r : ok({ sent: true });
  }

  /** Signs in with a code from email, SMS or WhatsApp (or a link's token hash). */
  async verifyOtp(
    p:
      | { type: "signup" | "magiclink" | "email" | "recovery" | "invite" | "email_change"; email: string; token: string }
      | { type: "sms" | "whatsapp" | "phone_change"; phone: string; token: string }
      | { type: string; tokenHash: string },
  ) {
    const body = "tokenHash" in p ? { type: p.type, token_hash: p.tokenHash } : p;
    const r = await json<Session>(this.bare(), "/auth/v1/verify", { body });
    return this.signedIn(r);
  }

  /** Another confirmation email or code. */
  async resend(p: { type: "signup" | "email_change"; email: string } | { type: "sms" | "phone_change"; phone: string }): Promise<Result<null>> {
    const r = await send(this.bare(), "/auth/v1/resend", { body: p });
    return r.error ? r : ok(null);
  }

  /** A password-reset link and code. */
  async resetPasswordForEmail(email: string, p: { redirectTo?: string; captchaToken?: string } = {}): Promise<Result<null>> {
    const r = await send(this.bare(), "/auth/v1/recover", { body: { email, redirect_to: p.redirectTo, captcha_token: p.captchaToken } });
    return r.error ? r : ok(null);
  }

  /**
   * The URL to send the user to for an OAuth provider (PKCE). In a browser
   * it goes there unless skipRedirect; on return, the session is picked up
   * from the URL (or call exchangeCodeForSession with the code).
   */
  async signInWithOAuth(p: { provider: OAuthProvider; redirectTo: string; scopes?: string; skipRedirect?: boolean }): Promise<Result<{ url: string }>> {
    const verifier = randomVerifier();
    const { challenge, method } = await challengeOf(verifier);
    await this.storage.setItem(this.storageKey + ".verifier", verifier);
    const url = buildURL(this.t.url, "/auth/v1/authorize", {
      provider: p.provider,
      redirect_to: p.redirectTo,
      scopes: p.scopes,
      code_challenge: challenge,
      code_challenge_method: method,
    });
    const loc = (globalThis as { location?: Location }).location;
    if (loc && !p.skipRedirect) loc.assign(url);
    return ok({ url });
  }

  /** Finishes an OAuth sign-in: the code from the redirect. */
  async exchangeCodeForSession(code: string) {
    const verifier = await this.storage.getItem(this.storageKey + ".verifier");
    if (!verifier) return fail<{ session: Session | null; user: User | null }>(new PgdockError(400, "no_code_verifier", "no sign-in was started from this app"));
    const r = await json<Session>(this.bare(), "/auth/v1/token", { query: { grant_type: "pkce" }, body: { auth_code: code, code_verifier: verifier } });
    await this.storage.removeItem(this.storageKey + ".verifier");
    return this.signedIn(r);
  }

  /** Ends this session (or the others, or all of them). */
  async signOut(p: { scope?: "local" | "others" | "global" } = {}): Promise<Result<null>> {
    const s = await this.getSession();
    if (s) {
      const r = await send(this.bare(), "/auth/v1/signout", { query: { scope: p.scope ?? "local" }, token: s.access_token });
      if (r.error && r.error.status !== 401) return r;
    }
    if ((p.scope ?? "local") !== "others") await this.save(null, "SIGNED_OUT");
    return ok(null);
  }

  // ---- The user -----------------------------------------------------------------

  /** The signed-in user, fresh from the API. */
  async getUser(): Promise<Result<User>> {
    const token = await this.accessToken();
    if (!token) return fail(new PgdockError(401, "no_session", "not signed in"));
    return json<User>(this.bare(), "/auth/v1/user", { token });
  }

  /** Change the password, metadata (merged), email or phone (each confirmed by a link or code). */
  async updateUser(p: { password?: string; data?: Record<string, unknown>; email?: string; phone?: string; channel?: "sms" | "whatsapp" }): Promise<Result<User>> {
    const token = await this.accessToken();
    if (!token) return fail(new PgdockError(401, "no_session", "not signed in"));
    const r = await json<User>(this.bare(), "/auth/v1/user", { method: "PATCH", body: p, token });
    if (!r.error && this.session) await this.save({ ...this.session, user: r.data }, "USER_UPDATED");
    return r;
  }

  /** Link another provider to the signed-in user: the URL to open. */
  async linkIdentity(p: { provider: OAuthProvider; redirectTo: string }): Promise<Result<{ url: string }>> {
    const token = await this.accessToken();
    return json<{ url: string }>(this.bare(), "/auth/v1/user/identities/authorize", {
      query: { provider: p.provider, redirect_to: p.redirectTo },
      token: token ?? "",
    });
  }

  async unlinkIdentity(id: string): Promise<Result<null>> {
    const token = await this.accessToken();
    const r = await send(this.bare(), "/auth/v1/user/identities/" + encodeURIComponent(id), { method: "DELETE", token: token ?? "" });
    return r.error ? r : ok(null);
  }

  // ---- MFA ----------------------------------------------------------------------

  readonly mfa = {
    /** A TOTP secret and otpauth:// URI, or a phone factor. */
    enroll: async (p: { factorType: "totp"; friendlyName?: string } | { factorType: "phone"; phone: string; friendlyName?: string }) =>
      json<{ id: string; type: string; totp?: { secret: string; uri: string; qr_code?: string } }>(this.bare(), "/auth/v1/factors", {
        body: { factor_type: p.factorType, friendly_name: p.friendlyName, phone: "phone" in p ? p.phone : undefined },
        token: (await this.accessToken()) ?? "",
      }),
    challenge: async (factorId: string) =>
      json<{ id: string; expires_at: number }>(this.bare(), `/auth/v1/factors/${encodeURIComponent(factorId)}/challenge`, {
        body: {},
        token: (await this.accessToken()) ?? "",
      }),
    /** Verifies a code: the session moves to aal2. */
    verify: async (p: { factorId: string; challengeId: string; code: string }) => {
      const r = await json<Session>(this.bare(), `/auth/v1/factors/${encodeURIComponent(p.factorId)}/verify`, {
        body: { challenge_id: p.challengeId, code: p.code },
        token: (await this.accessToken()) ?? "",
      });
      if (!r.error) await this.save(r.data, "MFA_VERIFIED");
      return r;
    },
    unenroll: async (factorId: string) => {
      const r = await send(this.bare(), `/auth/v1/factors/${encodeURIComponent(factorId)}`, {
        method: "DELETE",
        token: (await this.accessToken()) ?? "",
      });
      return r.error ? fail<null>(r.error) : ok(null);
    },
  };

  /** Stops the refresh timer (servers and tests). */
  stop() {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
  }
}

/** The exp claim of a JWT, unverified (the API verifies). */
export function jwtExp(token: string): number | undefined {
  try {
    const part = token.split(".")[1];
    if (!part) return undefined;
    const pad = part.replace(/-/g, "+").replace(/_/g, "/");
    const claims = JSON.parse(atob(pad + "=".repeat((4 - (pad.length % 4)) % 4)));
    return typeof claims.exp === "number" ? claims.exp : undefined;
  } catch {
    return undefined;
  }
}
