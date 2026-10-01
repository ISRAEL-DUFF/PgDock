// The app of the imported Supabase project, as a stand-in for the real one:
// a tiny HTTP API over its tables, using only the connection string PGDock
// handed out. "Serving its app" means these requests work after the import.
import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import type pg from "pg";

export type TodoApp = { url: string; close: () => Promise<void> };

export async function startTodoApp(connect: () => Promise<pg.Client>): Promise<TodoApp> {
  const query = async <T extends pg.QueryResultRow>(sql: string, params: unknown[] = []) => {
    const c = await connect();
    try {
      return (await c.query<T>(sql, params)).rows;
    } finally {
      await c.end();
    }
  };
  const server: Server = createServer(async (req, res) => {
    const u = new URL(req.url ?? "/", "http://app");
    try {
      let body: unknown;
      if (req.method === "GET" && u.pathname === "/todos") {
        body = await query("SELECT id::int, task, done FROM todos WHERE user_id = $1 ORDER BY id", [u.searchParams.get("user")]);
      } else if (req.method === "GET" && u.pathname === "/open") {
        body = await query("SELECT count(*)::int AS open FROM app.open_todos");
      } else if (req.method === "POST" && u.pathname === "/todos") {
        const chunks: Buffer[] = [];
        for await (const c of req) chunks.push(c as Buffer);
        const t = JSON.parse(Buffer.concat(chunks).toString()) as { user: string; task: string };
        body = (await query("INSERT INTO todos (user_id, task) VALUES ($1, $2) RETURNING id::int, ref", [t.user, t.task]))[0];
      } else if (req.method === "GET" && u.pathname === "/profile") {
        body = (await query("SELECT username FROM profiles WHERE id = $1", [u.searchParams.get("user")]))[0] ?? null;
      } else {
        res.writeHead(404).end();
        return;
      }
      res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify(body));
    } catch (e) {
      res.writeHead(500, { "Content-Type": "text/plain" }).end(String(e));
    }
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  return { url: `http://127.0.0.1:${port}`, close: () => new Promise((r) => server.close(() => r())) };
}
