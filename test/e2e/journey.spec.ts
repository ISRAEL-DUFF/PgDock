// The M2 and M3 "done when"s, entirely through the browser (spec §14): a
// fresh install to a working database; then back up, delete data, restore
// into a new project and verify it, and import a Supabase project and serve
// its app. Run against the install bundle by `make test-e2e`, or against a
// dev server with PGDOCK_E2E_URL.
import { expect, test, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import pg from "pg";
import { startTodoApp } from "./todo-app";
import { freshTotp } from "./totp";

const setupCode = process.env.PGDOCK_E2E_SETUP_CODE ?? "";
const dbHost = process.env.PGDOCK_E2E_DB_HOST ?? "127.0.0.1";
// Where the DB hostname really points (no DNS in CI): "host[:port]" pairs
// are rewritten to this address, keeping TLS hostname verification.
const dbAddr = process.env.PGDOCK_E2E_DB_ADDR;
const caFile = process.env.PGDOCK_E2E_DB_CA; // verify-full against this root
// Wait for the pooler certificate from this issuer (ACME) before connecting.
const expectIssuer = process.env.PGDOCK_E2E_EXPECT_ISSUER;
// Backup storage the wizard configures (the bundle's fake S3).
const s3Endpoint = process.env.PGDOCK_E2E_S3_ENDPOINT;
const s3Bucket = process.env.PGDOCK_E2E_S3_BUCKET ?? "pgdock-e2e";
// A Supabase project to import: seeded through seedURL, imported from
// sourceURL (the same database, as the control plane reaches it).
const supabaseSeedURL = process.env.PGDOCK_E2E_SUPABASE_SEED_URL;
const supabaseURL = process.env.PGDOCK_E2E_SUPABASE_URL;
const email = "owner@example.com";
const password = "a long enough passphrase";

let totpSecret = "";
// The signed-in session from the first test, reused by the later ones:
// signing in again would spend the per-address auth rate limit (10 per 5
// minutes) that the first test already exercises.
const stateFile = "test-results/.session.json";

// Optional screenshots of key screens, in both themes, for review.
const shotDir = process.env.PGDOCK_E2E_SCREENSHOTS;
async function shot(page: Page, name: string) {
  if (!shotDir) return;
  await page.screenshot({ path: `${shotDir}/${name}-light.png`, fullPage: true });
  await page.emulateMedia({ colorScheme: "dark" });
  await page.waitForTimeout(400); // let colour transitions finish
  await page.screenshot({ path: `${shotDir}/${name}-dark.png`, fullPage: true });
  await page.emulateMedia({ colorScheme: "light" });
  await page.waitForTimeout(400);
}

/** Opens the app with the session saved by the first test. */
async function signedIn(page: Page) {
  await page.goto("/projects");
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();
}

async function count(url: string, sql: string): Promise<number> {
  const c = await connect(url);
  try {
    const r = await c.query(sql);
    return Number(Object.values(r.rows[0])[0]);
  } finally {
    await c.end();
  }
}

/** Connects as a client app would, with the URL copied from the UI. */
async function connect(url: string): Promise<pg.Client> {
  const u = new URL(url);
  const client = new pg.Client({
    host: dbAddr ?? u.hostname,
    port: Number(u.port),
    user: decodeURIComponent(u.username),
    password: decodeURIComponent(u.password),
    database: u.pathname.slice(1),
    ssl: caFile
      ? { ca: readFileSync(caFile, "utf8"), servername: u.hostname, rejectUnauthorized: true }
      : { rejectUnauthorized: false }, // sslmode=require: encrypted, unverified
    connectionTimeoutMillis: 10_000,
  });
  // A dropped connection (e.g. the project was deleted) must not crash the run.
  client.on("error", () => {});
  await client.connect();
  return client;
}

async function psql(url: string, sql: string): Promise<string> {
  const u = new URL(url);
  const conninfo = [
    `host=${u.hostname}`,
    dbAddr ? `hostaddr=${dbAddr}` : "",
    `port=${u.port}`,
    `user=${decodeURIComponent(u.username)}`,
    `password=${decodeURIComponent(u.password)}`,
    `dbname=${u.pathname.slice(1)}`,
    caFile ? `sslmode=verify-full sslrootcert=${caFile}` : "sslmode=require",
  ]
    .filter(Boolean)
    .join(" ");
  const { stdout } = await promisify(execFile)("psql", [conninfo, "-Atc", sql]);
  return stdout.trim();
}

async function hasPsql(): Promise<boolean> {
  try {
    await promisify(execFile)("psql", ["--version"]);
    return true;
  } catch {
    return false;
  }
}

async function revealedValue(page: Page, testId: string): Promise<string> {
  const code = page.getByTestId(testId);
  const row = code.locator("..");
  const show = row.getByRole("button", { name: "Show" });
  if (await show.isVisible()) await show.click();
  return (await code.textContent())!.trim();
}

test.describe.configure({ mode: "serial" });

test("fresh install to a working database, entirely in the browser", async ({ page }) => {
  // 1. A fresh install sends every page to the setup wizard.
  await page.goto("/");
  await expect(page).toHaveURL(/\/setup$/);
  await expect(page.getByRole("heading", { name: "Create the owner account" })).toBeVisible();
  await shot(page, "01-setup");

  await page.getByLabel("Setup code").fill(setupCode);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password").fill(password);
  await page.getByRole("button", { name: "Continue" }).click();

  // 2. Enrol TOTP with the key the wizard shows (an authenticator app would scan the QR).
  await expect(page.getByRole("heading", { name: "Set up two-factor authentication" })).toBeVisible();
  await expect(page.getByAltText("TOTP QR code")).toBeVisible();
  await shot(page, "02-setup-totp");
  totpSecret = (await page.getByTestId("totp-secret").textContent())!.trim();
  expect(totpSecret).toMatch(/^[A-Z2-7]{32}$/);
  await page.getByLabel("Code").fill(await freshTotp(totpSecret));
  await page.getByRole("button", { name: "Verify and create account" }).click();

  // 3. The database hostname, with a DNS check.
  await expect(page.getByRole("heading", { name: "Database hostname" })).toBeVisible();
  await page.getByLabel("Hostname").fill(dbHost);
  await page.getByRole("button", { name: "Check DNS" }).click();
  await expect(page.getByText(/DNS (points at this server|does not point here yet)/)).toBeVisible();
  await page.getByRole("button", { name: "Save and continue" }).click();

  // 4. Backup storage, with the live write/read/delete test.
  await expect(page.getByRole("heading", { name: "Backup storage" })).toBeVisible();
  if (s3Endpoint) {
    await page.getByLabel("Endpoint").fill(s3Endpoint);
    await page.getByLabel("Region").fill("us-east-1");
    await page.getByLabel("Bucket").fill(s3Bucket);
    await page.getByLabel("Access key").fill("e2e-access");
    await page.getByLabel("Secret key").fill("e2e-secret");
    await page.getByLabel(/Path-style addressing/).check();
    await page.getByRole("button", { name: "Test only" }).click();
    await expect(page.getByText("Live test passed", { exact: true })).toBeVisible();
    await expect(page.getByTestId("storage-test-steps")).toContainText("delete");
    await shot(page, "09-setup-storage");
    await page.getByRole("button", { name: "Test, save, and continue" }).click();
  } else {
    await page.getByRole("button", { name: /Skip for now/ }).click();
  }

  // 5. The backup key must be downloaded and confirmed before continuing.
  await expect(page.getByRole("heading", { name: "Backup encryption key" })).toBeVisible();
  await page.getByRole("button", { name: "Generate backup key" }).click();
  await expect(page.getByRole("button", { name: "Confirm backup key" })).toBeDisabled();
  const [download] = await Promise.all([page.waitForEvent("download"), page.getByRole("button", { name: "Download key file" }).click()]);
  const keyFile = readFileSync((await download.path())!, "utf8");
  expect(keyFile).toMatch(/^pgdock-backup-key-v1:[A-Za-z0-9+/=]+$/m);
  await page.getByTestId("confirm-backup-key").fill("pgdock-backup-key-v1:" + "A".repeat(43) + "=");
  await page.getByRole("button", { name: "Confirm backup key" }).click();
  await expect(page.getByText("That is not this installation's backup key.")).toBeVisible();
  await page.getByLabel("Load key file").setInputFiles({ name: "key.txt", mimeType: "text/plain", buffer: Buffer.from(keyFile) });
  await shot(page, "10-setup-key");
  await page.getByRole("button", { name: "Confirm backup key" }).click();

  // 6. The bundle's agent registers the local node by itself.
  await expect(page.getByRole("heading", { name: "Local node" })).toBeVisible();
  if (s3Endpoint) {
    await expect(page.getByText("Agent connected")).toBeVisible({ timeout: 60_000 });
    await expect(page.getByTestId("agent-status").first()).toContainText("healthy");
    await shot(page, "11-setup-node");
    await page.getByRole("button", { name: "Continue", exact: true }).click();
  } else {
    await page.getByRole("button", { name: /Continue/ }).click();
  }
  await expect(page.getByRole("heading", { name: "PGDock is ready" })).toBeVisible();

  // The pooler certificate for the hostname arrives over ACME (HTTP-01).
  if (expectIssuer) {
    await page.goto("/settings");
    await expect(async () => {
      await page.reload();
      await expect(page.getByText(expectIssuer).first()).toBeVisible({ timeout: 2_000 });
    }).toPass({ timeout: 120_000 });
    await expect(page.getByText(dbHost, { exact: true }).first()).toBeVisible();
    await shot(page, "08-settings-tls");
    await page.goto("/projects/new");
  } else {
    // 7. Create the first project and watch it provision.
    await page.getByRole("button", { name: "Create your first project" }).click();
  }
  await expect(page).toHaveURL(/\/projects\/new$/);
  await page.getByLabel("Name").fill("My Blog");
  await page.getByRole("button", { name: "Create project" }).click();
  await expect(page.getByTestId("credential-panel")).toBeVisible();
  await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByTestId("operation-log")).toContainText("connected through the transaction pooler");
  await shot(page, "03-created");

  const pooledURL = await revealedValue(page, "credential-pooled-url");
  const sessionURL = await revealedValue(page, "credential-session-url");
  expect(pooledURL).toContain(`@${dbHost}:`);
  expect(pooledURL).toContain("sslmode=require");

  // 5. The copied URLs work for an app, over TLS.
  const app = await connect(pooledURL);
  await app.query("CREATE TABLE posts (id serial PRIMARY KEY, title text NOT NULL)");
  await app.query("INSERT INTO posts (title) VALUES ($1)", ["Hello from the browser"]);
  const { rows } = await app.query("SELECT current_database() AS db, (SELECT count(*) FROM posts)::int AS n");
  expect(rows[0].db).toMatch(/^my_blog_[a-z0-9]{4}$/);
  expect(rows[0].n).toBe(1);
  await app.end();
  if (await hasPsql()) {
    expect(await psql(sessionURL, "SELECT title FROM posts")).toBe("Hello from the browser");
  }

  await page.getByLabel("I've saved the password somewhere safe").check();
  await page.getByRole("button", { name: "Done" }).click();
  await page.getByRole("link", { name: "Open the project" }).click();
  await expect(page.getByRole("heading", { name: /My Blog/ })).toBeVisible();
  await shot(page, "04-overview");

  // 6. Connect page snippets carry the real host and database.
  const projectTabs = page.getByRole("navigation", { name: "Project" });
  await projectTabs.getByRole("link", { name: "Connect" }).click();
  await expect(page.getByText(`psql 'postgresql://${rows[0].db}_owner:YOUR_PASSWORD@${dbHost}`)).toBeVisible();
  await shot(page, "05-connect");

  // 7. Change a guardrail; it applies to the live database.
  await projectTabs.getByRole("link", { name: "Settings" }).click();
  await page.getByLabel("Statement timeout").fill("15s");
  await page.getByRole("button", { name: "Save guardrails" }).click();
  await expect(page.getByText("Saved; applying to the database and pooler.")).toBeVisible();
  await expect(async () => {
    const c = await connect(sessionURL);
    const r = await c.query("SHOW statement_timeout");
    await c.end();
    expect(r.rows[0].statement_timeout).toBe("15s");
  }).toPass({ timeout: 30_000 });

  // 8. Rotate the password: the new one works, the old one stops.
  await page.getByRole("button", { name: "Rotate password" }).click();
  await expect(page.getByTestId("credential-panel")).toBeVisible();
  const rotatedURL = await revealedValue(page, "credential-pooled-url");
  expect(rotatedURL).not.toBe(pooledURL);
  await expect(async () => {
    const c = await connect(rotatedURL);
    await c.end();
  }).toPass({ timeout: 30_000 });
  await expect(connect(pooledURL)).rejects.toThrow();
  await page.getByLabel("I've saved the password somewhere safe").check();
  await page.getByRole("button", { name: "Done" }).click();

  // 9. Operations and the audit log recorded it all.
  await page.getByRole("link", { name: "Operations", exact: true }).click();
  await expect(page.getByRole("link", { name: "rotate" }).first()).toBeVisible();
  await page.getByRole("link", { name: "create" }).first().click();
  await expect(page.getByTestId("operation-log")).toContainText("is active");
  await page.getByRole("link", { name: "Audit log" }).click();
  for (const action of ["setup.complete", "project.create", "project.update", "project.rotate_password"]) {
    await expect(page.getByRole("cell", { name: action, exact: true }).first()).toBeVisible();
  }
  await shot(page, "06-audit");

  // 10. Sign out and back in with password + TOTP.
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/login/);
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill("wrong password, clearly");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByRole("alert")).toContainText("invalid email, password, or code");
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Code").fill(await freshTotp(totpSecret));
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Projects" })).toBeVisible();

  // 11. Delete needs the typed name plus password and code.
  await page.getByRole("link", { name: "My Blog", exact: true }).click();
  await page.getByRole("navigation", { name: "Project" }).getByRole("link", { name: "Settings" }).click();
  await page.getByRole("button", { name: "Delete project" }).click();
  const dialog = page.getByRole("dialog");
  const confirm = dialog.getByRole("button", { name: "Delete project" });
  await expect(confirm).toBeDisabled();
  await shot(page, "07-delete-confirm");
  await dialog.getByTestId("confirm-name").fill("My Blog");
  await dialog.getByLabel("Your password").fill(password);
  await dialog.getByTestId("confirm-code").fill(await freshTotp(totpSecret));
  await confirm.click();
  await expect(page).toHaveURL(/\/projects$/);
  // A final backup runs first, so the database disappears after a moment.
  await expect(async () => {
    let c: pg.Client;
    try {
      c = await connect(rotatedURL);
    } catch {
      return;
    }
    await c.end();
    throw new Error("the deleted project still accepts connections");
  }).toPass({ timeout: 60_000 });
  await expect(page.getByText("No projects yet")).toBeVisible({ timeout: 30_000 });
  await page.context().storageState({ path: stateFile });
});

// The M3 "done when" (spec §14): delete data, restore to a new project, and
// verify it; then import a Supabase project and serve its app from PGDock.
// (A hosted Supabase project is not reachable from CI, so the import source
// is the supabase/postgres image with a Supabase-shaped app in it.)
test.describe("with the saved session", () => {
  test.use({ storageState: stateFile });

  test("back up, delete data, restore and verify; import a Supabase project and serve its app", async ({ page }) => {
    test.skip(!s3Endpoint || !supabaseSeedURL || !supabaseURL, "needs the e2e bundle's fake S3 and Supabase source");
    await signedIn(page);

    // 1. A project with data.
    await page.goto("/projects/new");
    await page.getByLabel("Name").fill("Notes");
    await page.getByRole("button", { name: "Create project" }).click();
    await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 60_000 });
    const notesURL = await revealedValue(page, "credential-pooled-url");
    await page.getByLabel("I've saved the password somewhere safe").check();
    await page.getByRole("button", { name: "Done" }).click();
    const app = await connect(notesURL);
    await app.query("CREATE TABLE notes (id serial PRIMARY KEY, body text NOT NULL)");
    await app.query("INSERT INTO notes (body) SELECT 'note ' || g FROM generate_series(1, 50) g");
    await app.end();

    // 2. Back it up from the Backups tab.
    await page.getByRole("link", { name: "Open the project" }).click();
    const tabs = page.getByRole("navigation", { name: "Project" });
    await tabs.getByRole("link", { name: "Backups" }).click();
    await expect(page.getByTestId("last-backup")).toHaveText("never");
    await page.getByRole("button", { name: "Back up now" }).click();
    await expect(page.getByTestId("backup-row").first()).toContainText("succeeded", { timeout: 60_000 });
    await expect(page.getByTestId("backup-row").first()).toContainText("Nightly / manual");
    await page.reload();
    await expect(page.getByTestId("last-backup")).toContainText(/just now|s ago/);
    await shot(page, "12-backups");

    // 3. Delete data.
    expect(await count(notesURL, "DELETE FROM notes WHERE id > 5 RETURNING 1")).toBe(1);
    expect(await count(notesURL, "SELECT count(*) FROM notes")).toBe(5);

    // 4. Restore into a new project and verify it.
    await page.getByTestId("backup-row").first().getByRole("button", { name: "Restore" }).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByLabel("New project name")).toHaveValue("Notes restored");
    await shot(page, "13-restore-dialog");
    await dialog.getByRole("button", { name: "Restore into new project" }).click();
    await expect(page.getByTestId("credential-panel")).toBeVisible();
    await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 60_000 });
    await expect(page.getByTestId("operation-log")).toContainText("restored in");
    const restoredURL = await revealedValue(page, "credential-pooled-url");
    expect(await count(restoredURL, "SELECT count(*) FROM notes")).toBe(50);
    expect(await count(restoredURL, "SELECT max(id) FROM notes")).toBe(50);
    expect(await count(notesURL, "SELECT count(*) FROM notes")).toBe(5); // the original is untouched
    await shot(page, "14-restored");
    await page.getByLabel("I've saved the password somewhere safe").check();
    await page.getByRole("button", { name: "Done" }).click();

    // 5. Restore in place: typed name, password and code; same URL afterwards.
    await page.goto("/projects");
    await page.getByRole("link", { name: "Notes", exact: true }).click();
    await tabs.getByRole("link", { name: "Backups" }).click();
    await page.getByTestId("backup-row").last().getByRole("button", { name: "Restore" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Restore in place instead…" }).click();
    const confirm = page.getByRole("dialog").getByRole("button", { name: "Restore in place" });
    await expect(confirm).toBeDisabled();
    await page.getByRole("dialog").getByTestId("confirm-name").fill("Notes");
    await page.getByRole("dialog").getByLabel("Your password").fill(password);
    await page.getByRole("dialog").getByTestId("confirm-code").fill(await freshTotp(totpSecret));
    await confirm.click();
    await expect(async () => {
      expect(await count(notesURL, "SELECT count(*) FROM notes")).toBe(50);
    }).toPass({ timeout: 60_000 });
    // A safety backup of the 5-row state was taken first.
    await expect(page.getByText("Safety (before restore)")).toBeVisible({ timeout: 30_000 });

    // 6. The projects list shows the last backup.
    await page.goto("/projects");
    const row = page.getByRole("row").filter({ has: page.getByRole("link", { name: "Notes", exact: true }) });
    await expect(row.getByTestId("last-backup-cell")).not.toContainText("never");
    await shot(page, "15-projects-last-backup");

    // 7. Import a Supabase project.
    const seed = new pg.Client({ connectionString: supabaseSeedURL });
    await seed.connect();
    await seed.query(readFileSync(fileURLToPath(new URL("./supabase-fixture.sql", import.meta.url)), "utf8"));
    await seed.end();

    await page.getByRole("link", { name: "Import" }).click();
    await page.getByLabel("Source connection string").fill(supabaseURL!);
    await page.getByRole("button", { name: "Run preflight" }).click();
    await expect(page.getByText("Supabase project")).toBeVisible({ timeout: 30_000 });
    await expect(page.getByTestId("preflight-version")).toContainText("17");
    await expect(page.getByLabel("Import schema public")).toBeChecked();
    await expect(page.getByLabel("Import schema app")).toBeChecked();
    await expect(page.getByLabel("Import schema auth")).not.toBeChecked();
    await expect(page.getByLabel("Import schema storage")).not.toBeChecked();
    await expect(page.getByRole("cell", { name: "own todos" })).toBeVisible();
    await shot(page, "16-import-preflight");
    await page.getByLabel("Project name").fill("Todo app");
    await page.getByRole("button", { name: "Import 2 schemas" }).click();
    await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 120_000 });
    await expect(page.getByTestId("operation-log")).toContainText("verified: 2 table(s), 122 row(s)");
    await shot(page, "17-imported");
    const todoURL = await revealedValue(page, "credential-pooled-url");
    expect(todoURL).toContain(`@${dbHost}:`);

    // 8. The app runs against PGDock with nothing but the new URL.
    const todo = await startTodoApp(() => connect(todoURL));
    try {
      const ada = "00000000-0000-0000-0000-000000000001";
      const list = (await (await fetch(`${todo.url}/todos?user=${ada}`)).json()) as { id: number }[];
      expect(list).toHaveLength(60);
      expect(await (await fetch(`${todo.url}/profile?user=${ada}`)).json()).toEqual({ username: "ada" });
      const created = (await (await fetch(`${todo.url}/todos`, { method: "POST", body: JSON.stringify({ user: ada, task: "written on PGDock" }) })).json()) as {
        id: number;
        ref: string;
      };
      expect(created.id).toBe(121); // the identity sequence carried over
      expect(created.ref).toMatch(/^[0-9a-f-]{36}$/); // extensions.uuid_generate_v4() works
      expect(await (await fetch(`${todo.url}/open`)).json()).toEqual([{ open: 81 }]);
    } finally {
      await todo.close();
    }
    const src = new pg.Client({ connectionString: supabaseSeedURL });
    await src.connect();
    expect((await src.query("SELECT count(*)::int AS n FROM public.todos")).rows[0].n).toBe(120); // the source was not modified
    await src.end();
  });

  // The M4 "done when" (spec §14): a dedicated project works through the
  // same pooler URL format, and a point-in-time restore succeeds, through
  // the browser. The bundle's local node runs dedicated instances too.
  test("a dedicated project through the same pooler URL format, and a point-in-time restore", async ({ page }) => {
    test.skip(!s3Endpoint, "needs the e2e bundle (Docker for instances, fake S3 for WAL-G)");
    await signedIn(page);

    // A shared project's URL, for comparison.
    const urlShape = (u: string) => {
      const x = new URL(u);
      return `${x.protocol}//${x.host}?${x.searchParams.toString()}`;
    };
    await page.goto("/projects/new");
    await page.getByLabel("Name").fill("Shape check");
    await page.getByRole("button", { name: "Create project" }).click();
    await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 60_000 });
    const sharedURL = await revealedValue(page, "credential-pooled-url");

    // 1. Create on the dedicated tier.
    await page.goto("/projects/new");
    await page.getByLabel("Name").fill("Orders Pro");
    await page.getByLabel("Dedicated").check();
    await expect(page.getByLabel("Size")).toContainText("small");
    await page.getByLabel("Volume (GB)").fill("5");
    await shot(page, "18-new-dedicated");
    await page.getByRole("button", { name: "Create project" }).click();
    await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 180_000 });
    await expect(page.getByTestId("operation-log")).toContainText("archiving WAL to s3://");
    await expect(page.getByTestId("operation-log")).toContainText("base backup base_");
    const pooledURL = await revealedValue(page, "credential-pooled-url");
    expect(urlShape(pooledURL)).toBe(urlShape(sharedURL)); // same host, ports, sslmode
    expect(new URL(pooledURL).pathname).toMatch(/^\/orders_pro_[a-z0-9]{4}$/);
    await page.getByLabel("I've saved the password somewhere safe").check();
    await page.getByRole("button", { name: "Done" }).click();

    // 2. The app writes through the pooler; remember a moment, then damage.
    const app = await connect(pooledURL);
    await app.query("CREATE TABLE orders (id bigserial PRIMARY KEY, item text NOT NULL)");
    await app.query("INSERT INTO orders (item) SELECT 'order ' || g FROM generate_series(1, 30) g");
    await new Promise((r) => setTimeout(r, 2_000));
    const target = new Date(Math.floor(Date.now() / 1000) * 1000); // datetime-local has second precision
    await new Promise((r) => setTimeout(r, 2_000));
    await app.query("DELETE FROM orders WHERE id > 3");
    await app.end();
    expect(await count(pooledURL, "SELECT count(*) FROM orders")).toBe(3);

    // 3. The instance card and the node it runs on.
    await page.getByRole("link", { name: "Open the project" }).click();
    await expect(page.getByTestId("instance-card")).toContainText("small");
    await expect(page.getByTestId("instance-card")).toContainText("5 GB volume");
    await shot(page, "19-dedicated-overview");

    // 4. Point-in-time recovery into a new project, from the Backups tab.
    await page.getByRole("navigation", { name: "Project" }).getByRole("link", { name: "Backups" }).click();
    await expect(page.getByTestId("backup-row").first()).toContainText("Base backup (WAL-G)");
    await expect(page.getByTestId("pitr-window")).toBeVisible();
    const pad = (n: number) => String(n).padStart(2, "0");
    const local = `${target.getFullYear()}-${pad(target.getMonth() + 1)}-${pad(target.getDate())}T${pad(target.getHours())}:${pad(target.getMinutes())}:${pad(target.getSeconds())}`;
    await page.getByLabel("Restore to (your local time)").fill(local);
    await page.getByLabel("New project name").fill("Orders Pro restored");
    await shot(page, "20-pitr");
    await page.getByRole("button", { name: "Restore to this point" }).click();
    await expect(page.getByTestId("credential-panel")).toBeVisible();
    await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 240_000 });
    await expect(page.getByTestId("operation-log")).toContainText("recovery finished and promoted");
    const restoredURL = await revealedValue(page, "credential-pooled-url");
    expect(urlShape(restoredURL)).toBe(urlShape(sharedURL));
    expect(await count(restoredURL, "SELECT count(*) FROM orders")).toBe(30);
    expect(await count(pooledURL, "SELECT count(*) FROM orders")).toBe(3); // the source is unchanged
    await shot(page, "21-pitr-restored");
    await page.getByLabel("I've saved the password somewhere safe").check();
    await page.getByRole("button", { name: "Done" }).click();

    // 5. The node page lists both dedicated instances.
    await page.getByRole("link", { name: "Nodes", exact: true }).click();
    await page.getByRole("link", { name: "local" }).click();
    await expect(page.getByTestId("node-docker")).toHaveText("ok");
    await expect(page.getByTestId("instance-row").filter({ hasText: "dedicated" })).toHaveCount(2);
    await shot(page, "22-node");
  });
  // The M5 "done when" (spec §14): a hobby project is promoted with its URL
  // unchanged and no lost commits, while an app keeps writing.
  test("promote a hobby project with a live writer: same URL, no lost commits", async ({ page }) => {
    test.skip(!s3Endpoint, "needs the e2e bundle (Docker for instances, fake S3 for WAL-G)");
    await signedIn(page);

    await page.goto("/projects/new");
    await page.getByLabel("Name").fill("Hobby promo");
    await page.getByRole("button", { name: "Create project" }).click();
    await expect(page.getByTestId("provision-ready")).toBeVisible({ timeout: 60_000 });
    const pooledURL = await revealedValue(page, "credential-pooled-url");
    await page.getByLabel("I've saved the password somewhere safe").check();
    await page.getByRole("button", { name: "Done" }).click();
    const seed = await connect(pooledURL);
    await seed.query("CREATE TABLE events (id bigserial PRIMARY KEY, n int UNIQUE NOT NULL)");
    await seed.query("CREATE TABLE notes AS SELECT g AS id, 'note ' || g AS body FROM generate_series(1, 5000) g");
    await seed.end();

    // A live writer on the pooled URL, as an app would be.
    const acked: number[] = [];
    const errors: string[] = [];
    let stop = false;
    const writer = (async () => {
      let c: pg.Client | null = null;
      for (let n = 1; !stop; n++) {
        try {
          c ??= await connect(pooledURL);
          await c.query("INSERT INTO events (n) VALUES ($1)", [n]);
          acked.push(n);
        } catch (e) {
          errors.push(String(e));
          await c?.end().catch(() => {});
          c = null;
          await new Promise((r) => setTimeout(r, 100));
        }
        await new Promise((r) => setTimeout(r, 10));
      }
      await c?.end().catch(() => {});
    })();
    await expect.poll(() => acked.length).toBeGreaterThan(20);

    // The wizard: target, size, the estimate, then live progress.
    await page.getByRole("link", { name: "Open the project" }).click();
    await page.getByRole("navigation", { name: "Project" }).getByRole("link", { name: "Settings" }).click();
    await page.getByRole("button", { name: "Promote…" }).click();
    await expect(page.getByText(/Estimated write freeze: about \d+ s/)).toBeVisible();
    await expect(page.getByTestId("promote-estimate")).toContainText("The database is");
    await page.getByLabel("Volume size (GB)").fill("5");
    await shot(page, "23-promote-wizard");
    await page.getByRole("button", { name: "Promote now" }).click();
    await expect(page.getByTestId("promote-done")).toBeVisible({ timeout: 240_000 });
    await expect(page.getByTestId("operation-log")).toContainText("verified:");
    await expect(page.getByTestId("operation-log")).toContainText("writes were frozen for");
    await shot(page, "24-promoted");

    // The writer keeps going on the new instance, then stops.
    const after = acked.length;
    await expect.poll(() => acked.length, { timeout: 30_000 }).toBeGreaterThan(after + 20);
    stop = true;
    await writer;

    // Same URL, now on the dedicated instance, with every acknowledged commit.
    const c = await connect(pooledURL);
    const archive = (await c.query("SELECT current_setting('archive_mode') AS a")).rows[0].a;
    expect(archive).toBe("on");
    const have = new Set((await c.query("SELECT n FROM events")).rows.map((r: { n: number }) => r.n));
    expect(acked.filter((n) => !have.has(n))).toEqual([]);
    expect(Number((await c.query("SELECT count(*) AS n FROM notes")).rows[0].n)).toBe(5000);
    await c.end();
    expect(errors).toEqual([]); // the pooled URL waited out the freeze

    await page.getByRole("navigation", { name: "Project" }).getByRole("link", { name: "Overview" }).click();
    await expect(page.getByText("Promoted to the dedicated tier")).toBeVisible();
    await expect(page.getByTestId("instance-card")).toBeVisible();
    await shot(page, "25-promoted-overview");
  });
});
