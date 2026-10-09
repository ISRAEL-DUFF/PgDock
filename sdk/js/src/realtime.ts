import { PgdockError } from "./errors.js";

/** A database change as realtime delivers it. */
export interface Change<Row = Record<string, unknown>> {
  type: "INSERT" | "UPDATE" | "DELETE";
  schema: string;
  table: string;
  commit_timestamp: string;
  /** The row as the subscriber sees it (empty for a delete). */
  record: Row;
  /** The primary key, for updates and deletes. */
  old_record: Partial<Row>;
  columns?: { name: string; type: string }[];
}

export interface ChangeFilter {
  event?: "*" | "INSERT" | "UPDATE" | "DELETE";
  schema?: string;
  table: string;
  /** column=eq.value, neq, lt, lte, gt, gte, in.(a,b) */
  filter?: string;
}

export type PresenceState<T = Record<string, unknown>> = Record<
  string,
  (T & { phx_ref: string })[]
>;

export type ChannelStatus =
  | "SUBSCRIBED"
  | "CLOSED"
  | "CHANNEL_ERROR"
  | "TIMED_OUT";

interface Msg {
  topic: string;
  event: string;
  payload: any;
  ref: string | null;
  join_ref?: string | null;
}

type WebSocketLike = {
  readyState: number;
  send(data: string): void;
  close(code?: number, reason?: string): void;
  onopen: ((ev: unknown) => void) | null;
  onclose: ((ev: unknown) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
};

export type WebSocketCtor = new (url: string) => WebSocketLike;

export interface RealtimeOptions {
  /** A WebSocket implementation (the global one by default; Node ≥ 22 and browsers have it). */
  WebSocket?: WebSocketCtor;
  /** Heartbeat interval (25 s). */
  heartbeatMs?: number;
  /** Reconnect delays, growing to the last (1, 2, 5, 10 s). */
  reconnectMs?: number[];
}

/** One channel: database changes, broadcast and presence on a topic. */
export class Channel {
  private changeSubs: {
    f: ChangeFilter;
    cb: (c: Change<any>) => void;
    id?: number;
  }[] = [];
  private bcSubs: {
    event: string;
    cb: (payload: any, event: string) => void;
  }[] = [];
  private presenceSyncs: ((state: PresenceState) => void)[] = [];
  private presenceJoins: ((key: string, joined: any[]) => void)[] = [];
  private presenceLeaves: ((key: string, left: any[]) => void)[] = [];
  private resyncs: (() => void)[] = [];
  private statusCb?: (s: ChannelStatus, err?: PgdockError) => void;
  private pending = new Map<string, (status: string, response: any) => void>();
  private tracked: Record<string, unknown> | null = null;
  /** The presence state on this channel. */
  presence: PresenceState = {};
  joinRef: string | null = null;
  joined = false;
  private wanted = false;

  constructor(
    private rt: RealtimeClient,
    readonly name: string,
    private opts: {
      private?: boolean;
      broadcast?: { self?: boolean; ack?: boolean };
      presenceKey?: string;
    },
  ) {}

  get topic(): string {
    return "realtime:" + this.name;
  }

  /** Database changes on a table (turn realtime on for it first). */
  onChange<Row = Record<string, unknown>>(
    f: ChangeFilter,
    cb: (change: Change<Row>) => void,
  ): this {
    this.changeSubs.push({ f, cb });
    return this;
  }
  /** Broadcast messages with this event ("*" for all). */
  onBroadcast<T = unknown>(
    event: string,
    cb: (payload: T, event: string) => void,
  ): this {
    this.bcSubs.push({ event, cb });
    return this;
  }
  onPresenceSync(cb: (state: PresenceState) => void): this {
    this.presenceSyncs.push(cb);
    return this;
  }
  onPresenceJoin(cb: (key: string, joined: any[]) => void): this {
    this.presenceJoins.push(cb);
    return this;
  }
  onPresenceLeave(cb: (key: string, left: any[]) => void): this {
    this.presenceLeaves.push(cb);
    return this;
  }
  /**
   * The server dropped changes (too many, or its database connection was
   * lost), or this client reconnected: refetch what you show.
   */
  onResync(cb: () => void): this {
    this.resyncs.push(cb);
    return this;
  }

  /** Joins (and rejoins after reconnects). */
  subscribe(cb?: (status: ChannelStatus, err?: PgdockError) => void): this {
    this.statusCb = cb;
    this.wanted = true;
    this.rt.add(this);
    return this;
  }

  /** Leaves the channel. */
  async unsubscribe(): Promise<void> {
    this.wanted = false;
    if (this.joined) await this.push("phx_leave", {}).catch(() => undefined);
    this.joined = false;
    this.rt.remove(this);
  }

  /** Sends a broadcast to the channel's other members. */
  send(event: string, payload: unknown): Promise<void> {
    const p = this.push(
      "broadcast",
      { type: "broadcast", event, payload },
      !!this.opts.broadcast?.ack,
    );
    return p.then(() => undefined);
  }

  /** Shares this client's state with the channel. */
  track(state: Record<string, unknown>): Promise<void> {
    this.tracked = state;
    return this.push(
      "presence",
      { type: "presence", event: "track", payload: state },
      false,
    ).then(() => undefined);
  }

  untrack(): Promise<void> {
    this.tracked = null;
    return this.push(
      "presence",
      { type: "presence", event: "untrack" },
      false,
    ).then(() => undefined);
  }

  // ---- The protocol ---------------------------------------------------------------

  /** push sends an event; with wait, resolves on its reply. */
  push(event: string, payload: unknown, wait = true): Promise<any> {
    const ref = this.rt.nextRef();
    const msg: Msg = {
      topic: this.topic,
      event,
      payload,
      ref,
      join_ref: this.joinRef,
    };
    if (!wait) {
      this.rt.sendMsg(msg);
      return Promise.resolve(null);
    }
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(ref);
        reject(
          new PgdockError(
            0,
            "timeout",
            `${event} on ${this.name} got no reply`,
          ),
        );
      }, 15_000);
      this.pending.set(ref, (status, response) => {
        clearTimeout(timer);
        if (status === "ok") resolve(response);
        else
          reject(
            new PgdockError(
              0,
              "realtime_error",
              typeof response?.reason === "string"
                ? response.reason
                : JSON.stringify(response),
            ),
          );
      });
      this.rt.sendMsg(msg);
    });
  }

  /** join runs on (re)connect. */
  async join(token: string | null): Promise<void> {
    if (!this.wanted) return;
    this.joinRef = this.rt.nextRef();
    const config = {
      broadcast: {
        self: !!this.opts.broadcast?.self,
        ack: !!this.opts.broadcast?.ack,
      },
      presence: { key: this.opts.presenceKey ?? "" },
      postgres_changes: this.changeSubs.map((s) => ({
        event: s.f.event ?? "*",
        schema: s.f.schema ?? "public",
        table: s.f.table,
        filter: s.f.filter,
      })),
      private: !!this.opts.private,
    };
    const ref = this.joinRef;
    try {
      const resp = await new Promise<any>((resolve, reject) => {
        const timer = setTimeout(() => {
          this.pending.delete(ref);
          reject(
            new PgdockError(
              0,
              "timeout",
              "joining " + this.name + " timed out",
            ),
          );
        }, 15_000);
        this.pending.set(ref, (status, response) => {
          clearTimeout(timer);
          if (status === "ok") resolve(response);
          else
            reject(
              new PgdockError(
                0,
                "join_refused",
                typeof response?.reason === "string"
                  ? response.reason
                  : "the join was refused",
              ),
            );
        });
        this.rt.sendMsg({
          topic: this.topic,
          event: "phx_join",
          payload: { config, access_token: token ?? undefined },
          ref,
          join_ref: ref,
        });
      });
      const bound: {
        id: number;
        table: string;
        schema: string;
        event: string;
        filter?: string;
      }[] = resp?.postgres_changes ?? [];
      bound.forEach((b, i) => {
        const s = this.changeSubs[i];
        if (s) s.id = b.id;
      });
      this.joined = true;
      if (this.tracked) void this.track(this.tracked);
      this.statusCb?.("SUBSCRIBED");
    } catch (e) {
      this.joined = false;
      const err =
        e instanceof PgdockError
          ? e
          : new PgdockError(0, "join_refused", String(e));
      this.statusCb?.(
        err.code === "timeout" ? "TIMED_OUT" : "CHANNEL_ERROR",
        err,
      );
    }
  }

  /** handle takes a message for this topic. */
  handle(m: Msg) {
    switch (m.event) {
      case "phx_reply": {
        const cb = m.ref ? this.pending.get(m.ref) : undefined;
        if (cb && m.ref) {
          this.pending.delete(m.ref);
          cb(m.payload?.status, m.payload?.response);
        }
        return;
      }
      case "postgres_changes": {
        const ids: number[] = m.payload?.ids ?? [];
        const data = m.payload?.data as Change;
        for (const s of this.changeSubs) {
          if (s.id !== undefined && ids.includes(s.id)) s.cb(data);
        }
        return;
      }
      case "broadcast": {
        const ev = m.payload?.event as string;
        for (const s of this.bcSubs)
          if (s.event === "*" || s.event === ev) s.cb(m.payload?.payload, ev);
        return;
      }
      case "presence_state": {
        this.presence = {};
        for (const [k, v] of Object.entries(m.payload ?? {}))
          this.presence[k] = ((v as any).metas ?? []) as any[];
        for (const cb of this.presenceSyncs) cb(this.presence);
        return;
      }
      case "presence_diff": {
        for (const [k, v] of Object.entries(m.payload?.joins ?? {})) {
          const metas = ((v as any).metas ?? []) as any[];
          this.presence[k] = [...(this.presence[k] ?? []), ...metas];
          for (const cb of this.presenceJoins) cb(k, metas);
        }
        for (const [k, v] of Object.entries(m.payload?.leaves ?? {})) {
          const metas = ((v as any).metas ?? []) as any[];
          const gone = new Set(metas.map((x) => x.phx_ref));
          const rest = (this.presence[k] ?? []).filter(
            (x) => !gone.has(x.phx_ref),
          );
          if (rest.length) this.presence[k] = rest;
          else delete this.presence[k];
          for (const cb of this.presenceLeaves) cb(k, metas);
        }
        for (const cb of this.presenceSyncs) cb(this.presence);
        return;
      }
      case "system": {
        if (m.payload?.message === "resync") this.resync();
        else if (m.payload?.status === "error")
          this.statusCb?.(
            "CHANNEL_ERROR",
            new PgdockError(0, "realtime_error", String(m.payload?.message)),
          );
        return;
      }
      case "phx_close":
        this.joined = false;
        if (this.wanted) this.statusCb?.("CLOSED");
        return;
      case "phx_error":
        this.joined = false;
        this.statusCb?.(
          "CHANNEL_ERROR",
          new PgdockError(0, "realtime_error", "the channel failed"),
        );
        return;
    }
  }

  resync() {
    for (const cb of this.resyncs) {
      try {
        cb();
      } catch {
        // the callback's own
      }
    }
  }

  /** Forget replies we'll never get (the socket closed). */
  dropPending() {
    for (const cb of this.pending.values())
      cb("error", { reason: "the connection closed" });
    this.pending.clear();
    this.joined = false;
  }
}

/** client.realtime: one WebSocket for every channel. */
export class RealtimeClient {
  private ws: WebSocketLike | null = null;
  private channels = new Set<Channel>();
  private ref = 0;
  private heartbeat: ReturnType<typeof setInterval> | undefined;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private attempts = 0;
  private outbox: string[] = [];
  private everConnected = false;
  private closedByUs = false;
  private WS?: WebSocketCtor;

  constructor(
    private url: string,
    private key: string,
    private token: () => Promise<string | null>,
    private opts: RealtimeOptions = {},
  ) {
    this.WS =
      opts.WebSocket ??
      ((globalThis as { WebSocket?: WebSocketCtor }).WebSocket as
        | WebSocketCtor
        | undefined);
  }

  /** A channel; call subscribe() on it. */
  channel(
    name: string,
    opts: {
      private?: boolean;
      broadcast?: { self?: boolean; ack?: boolean };
      presenceKey?: string;
    } = {},
  ): Channel {
    return new Channel(this, name, opts);
  }

  nextRef(): string {
    this.ref += 1;
    return String(this.ref);
  }

  add(ch: Channel) {
    this.channels.add(ch);
    if (this.ws?.readyState === 1) void this.token().then((t) => ch.join(t));
    else this.connect();
  }

  remove(ch: Channel) {
    this.channels.delete(ch);
    if (this.channels.size === 0) this.disconnect();
  }

  sendMsg(m: Msg) {
    const s = JSON.stringify(m);
    if (this.ws?.readyState === 1) this.ws.send(s);
    else {
      this.outbox.push(s);
      this.connect();
    }
  }

  /** Tells every channel about a new access token (after a refresh or sign-in). */
  async setToken(token: string | null) {
    for (const ch of this.channels) {
      if (ch.joined)
        ch.push(
          "access_token",
          { access_token: token ?? this.key },
          false,
        ).catch(() => undefined);
    }
  }

  private connect() {
    if (this.ws || this.closedByUs || this.reconnectTimer) return;
    if (!this.WS)
      throw new PgdockError(
        0,
        "realtime_unavailable",
        "no WebSocket implementation; pass realtime.WebSocket",
      );
    const u = new URL(this.url.replace(/\/+$/, "") + "/realtime/v1/websocket");
    u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
    u.searchParams.set("apikey", this.key);
    u.searchParams.set("vsn", "1.0.0");
    const ws = new this.WS(u.toString());
    this.ws = ws;
    ws.onopen = () => {
      this.attempts = 0;
      const again = this.everConnected;
      this.everConnected = true;
      this.heartbeat = setInterval(() => {
        this.sendMsg({
          topic: "phoenix",
          event: "heartbeat",
          payload: {},
          ref: this.nextRef(),
        });
      }, this.opts.heartbeatMs ?? 25_000);
      (this.heartbeat as { unref?: () => void }).unref?.();
      void this.token().then(async (t) => {
        for (const ch of this.channels) {
          await ch.join(t);
          if (again) ch.resync(); // changes during the gap were missed
        }
        const out = this.outbox;
        this.outbox = [];
        for (const s of out) ws.send(s);
      });
    };
    ws.onmessage = (ev) => {
      let m: Msg;
      try {
        m = JSON.parse(String(ev.data));
      } catch {
        return;
      }
      for (const ch of this.channels) if (ch.topic === m.topic) ch.handle(m);
    };
    ws.onclose = () => this.lost();
    ws.onerror = () => undefined; // onclose follows
  }

  private lost() {
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.heartbeat = undefined;
    this.ws = null;
    for (const ch of this.channels) ch.dropPending();
    if (this.closedByUs || this.channels.size === 0) return;
    const delays = this.opts.reconnectMs ?? [1000, 2000, 5000, 10000];
    const d = delays[Math.min(this.attempts, delays.length - 1)] ?? 10000;
    this.attempts += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      this.connect();
    }, d);
  }

  private disconnect() {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = undefined;
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.heartbeat = undefined;
    const ws = this.ws;
    this.ws = null;
    this.everConnected = false;
    if (ws) {
      ws.onclose = null;
      ws.close();
    }
  }

  /** Closes the socket and every channel. */
  close() {
    this.closedByUs = true;
    this.channels.clear();
    this.disconnect();
  }

  /** Whether the socket is open. */
  get connected(): boolean {
    return this.ws?.readyState === 1;
  }
}
