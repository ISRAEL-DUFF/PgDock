import type { components } from "./schema";

type S = components["schemas"];
export type Version = S["Version"];
export type TableInfo = S["TableInfo"];
export type EditColumn = S["EditColumn"];
export type RowChange = S["RowChange"];
export type SaveRowsResult = S["SaveRowsResult"];
// Fields with server-side defaults are optional in requests.
export type SchemaChange = Omit<S["SchemaChange"], "schema" | "concurrently" | "columns" | "column"> & {
  schema?: string;
  concurrently?: boolean;
  columns?: SchemaColumnDef[];
  column?: SchemaColumnDef;
};
export type SchemaColumnDef = Omit<S["SchemaColumnDef"], "nullable"> & { nullable?: boolean };
export type SchemaPlan = S["SchemaPlan"];
export type MigrationFormat = S["EditorPreferences"]["migration_format"];
export type GridFilter = { column: string; op: "eq" | "neq" | "lt" | "lte" | "gt" | "gte" | "contains" | "is_null" | "not_null" | "in"; value?: string; values?: string[] };
export type GridOptions = { filters?: GridFilter[]; sort?: string; desc?: boolean };

function tableBase(id: string, schema: string, table: string) {
  return `/api/v1/projects/${id}/tables/${encodeURIComponent(schema)}/${encodeURIComponent(table)}`;
}

/** The grid's query string: repeated filter=JSON, sort, desc, and extras. */
function gridQs(g: GridOptions, extra: Record<string, string | undefined>): string {
  const p = new URLSearchParams();
  for (const f of g.filters ?? []) p.append("filter", JSON.stringify(f));
  if (g.sort) {
    p.set("sort", g.sort);
    if (g.desc) p.set("desc", "true");
  }
  for (const [k, v] of Object.entries(extra)) if (v !== undefined) p.set(k, v);
  const s = p.toString();
  return s ? `?${s}` : "";
}
export type APIToken = S["APIToken"];
export type TokenScope = S["TokenScope"];
export type CreatedToken = S["CreatedToken"];
export type DeviceRequest = S["DeviceRequest"];
export type ApiErrorBody = S["Error"];
export type SessionState = S["SessionState"];
export type Project = S["Project"];
export type ProjectSettings = S["ProjectSettings"];
export type ProjectCredentials = S["ProjectCredentials"];
export type ProjectUpdated = S["ProjectUpdated"];
export type Operation = S["Operation"];
export type OperationLogEntry = S["OperationLogEntry"];
export type AuditEntry = S["AuditEntry"];
export type AuditList = S["AuditList"];
export type GeneralSettings = S["GeneralSettings"];
export type DnsCheck = S["DnsCheck"];
export type SetupEnrollment = S["SetupEnrollment"];
export type LoginChallenge = S["LoginChallenge"];
export type Backup = S["Backup"];
export type BackupOverview = S["BackupOverview"];
export type BackupKeyInfo = S["BackupKeyInfo"];
export type BackupKeyExport = S["BackupKeyExport"];
export type StorageRequest = S["StorageRequest"];
export type StorageSettings = S["StorageSettings"];
export type StorageTarget = S["StorageTarget"];
export type ProjectBackupStorage = S["ProjectBackupStorage"];
export type StorageTestResult = S["StorageTestResult"];
export type RestoreResponse = S["RestoreResponse"];
export type ImportPreflight = S["ImportPreflight"];
export type Node = S["Node"];
export type RegistrationToken = S["RegistrationToken"];
export type NodeDetail = S["NodeDetail"];
export type NodeCreated = S["NodeCreated"];
export type ProfileList = S["ProfileList"];
export type InstanceSummary = S["InstanceSummary"];
export type CreateProjectRequest = S["CreateProjectRequest"];
export type SqlResult = S["SqlResult"];
export type SqlStatementResult = S["SqlStatementResult"];
export type DbSchema = S["DbSchema"];
export type DbTable = S["DbTable"];
export type TablePage = S["TablePage"];
export type Extension = S["Extension"];
export type MetricsResponse = S["MetricsResponse"];
export type MetricRange = "1h" | "24h" | "7d";
export type AlertItem = S["Alert"];
export type AlertSettings = S["AlertSettings"];
export type AlertSettingsRequest = S["AlertSettingsRequest"];
export type IsolationCheck = S["IsolationCheck"];
export type User = S["User"];
export type Org = S["Org"];
export type OrgRole = S["OrgRole"];
export type ProjectRole = S["ProjectRole"];
export type OrgMember = S["OrgMember"];
export type Invitation = S["Invitation"];
export type InvitationCreated = S["InvitationCreated"];
export type InvitationPreview = S["InvitationPreview"];
export type ProjectMember = S["ProjectMember"];
export type PersonalCredentials = S["PersonalCredentials"];
export type PersonalCredentialsInfo = S["PersonalCredentialsInfo"];
export type AdminUser = S["AdminUser"];
export type MailSettings = S["MailSettings"];
export type MailSettingsRequest = S["MailSettingsRequest"];
export type SignupSettings = S["SignupSettings"];
export type Terms = S["Terms"];
export type SessionInfo = S["SessionInfo"];
export type OrgQuotas = S["OrgQuotas"];
export type QuotaItem = S["QuotaItem"];
export type UsageReport = S["UsageReport"];
export type UsageRecord = S["UsageRecord"];
export type DedicatedRequest = S["DedicatedRequest"];
export type DemotePreflight = S["DemotePreflight"];
export type Webhook = S["Webhook"];
export type WebhookRequest = S["WebhookRequest"];
export type WebhookDelivery = S["WebhookDelivery"];
export type Job = S["Job"];
export type JobRequest = S["JobRequest"];
export type JobRun = S["JobRun"];
export type DemoteRequest = S["DemoteRequest"];
export type BreakGlassSession = S["BreakGlassSession"];
export type ProjectStorage = S["ProjectStorage"];
export type StorageState = S["StorageState"];
export type ReapedSession = S["ReapedSession"];
export type SwitchedCredentials = S["SwitchedCredentials"];
export type AdminOrg = S["AdminOrg"];
export type AdminOrgSummary = S["AdminOrgSummary"];
export type Plan = S["Plan"];
export type SharedCluster = S["SharedCluster"];
export type PlatformUsage = S["PlatformUsage"];
export type AuditQuery = { action?: string; outcome?: string; target_id?: string; before?: number; limit?: number };

/** An error response from the API, with the server's error code. */
export class ApiRequestError extends Error {
  constructor(
    readonly status: number,
    readonly body: ApiErrorBody | undefined,
  ) {
    super(body?.message ?? `request failed with status ${status}`);
  }

  get code(): string | undefined {
    return this.body?.code;
  }
}

let csrfToken = "";

/** Remembers the CSRF token from /api/v1/session for mutating requests. */
export function setCsrfToken(token: string) {
  csrfToken = token;
}

/**
 * The CSRF cookie as the browser holds it now. Concurrent first requests can
 * each be issued a token, and the cookie keeps the last one, so it wins over
 * the remembered token.
 */
function cookieToken(): string {
  if (typeof document === "undefined") return "";
  const m = /(?:^|;\s*)(?:__Host-)?pgdock_csrf=([^;]+)/.exec(document.cookie);
  return m ? decodeURIComponent(m[1]) : "";
}

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

export async function request<T>(method: Method, path: string, body?: unknown, fetchFn: typeof fetch = fetch): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET") {
    let token = cookieToken() || csrfToken;
    // A page that writes on load (an invitation or email link) may get here
    // before anything has handed out a CSRF token.
    if (!token) {
      const s = await request<{ csrf_token: string }>("GET", "/api/v1/session", undefined, fetchFn);
      csrfToken ||= s.csrf_token;
      token = cookieToken() || csrfToken;
    }
    headers["X-CSRF-Token"] = token;
  }
  const res = await fetchFn(path, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let err: ApiErrorBody | undefined;
    try {
      err = (await res.json()) as ApiErrorBody;
    } catch {
      err = undefined;
    }
    throw new ApiRequestError(res.status, err);
  }
  // Some successes carry no body: 204, and 202 for requests that answer the
  // same whatever happened (sign-up, password reset).
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text === "" ? undefined : JSON.parse(text)) as T;
}

export function getJSON<T>(path: string, fetchFn?: typeof fetch): Promise<T> {
  return request<T>("GET", path, undefined, fetchFn);
}

export function getVersion(fetchFn?: typeof fetch): Promise<Version> {
  return getJSON<Version>("/api/v1/version", fetchFn);
}

function qs(params: Record<string, string | number | undefined | null>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export const api = {
  session: () => getJSON<SessionState>("/api/v1/session"),
  setupBegin: (b: { setup_code: string; email: string; password: string }) =>
    request<SetupEnrollment>("POST", "/api/v1/setup/begin", b),
  setupComplete: (b: { enrollment_token: string; code: string }) =>
    request<SessionState>("POST", "/api/v1/setup/complete", b),
  login: (b: { email: string; password: string }) => request<LoginChallenge>("POST", "/api/v1/auth/login", b),
  totp: (b: { challenge_id: string; code: string }) => request<SessionState>("POST", "/api/v1/auth/totp", b),
  reauth: (b: { password: string; code: string }) => request<void>("POST", "/api/v1/auth/reauth", b),
  logout: () => request<void>("POST", "/api/v1/auth/logout"),

  signup: (b: S["SignupRequest"]) => request<void>("POST", "/api/v1/auth/signup", b),
  verifyEmail: (token: string) => request<S["VerifyEmailResult"]>("POST", "/api/v1/auth/verify-email", { token }),
  resendVerification: (email: string) => request<void>("POST", "/api/v1/auth/verify-email/resend", { email }),
  requestPasswordReset: (email: string) => request<void>("POST", "/api/v1/auth/password-reset", { email }),
  confirmPasswordReset: (token: string, password: string) => request<void>("POST", "/api/v1/auth/password-reset/confirm", { token, password }),
  terms: () => getJSON<Terms>("/api/v1/terms"),
  acceptTerms: (version: number) => request<void>("POST", "/api/v1/me/terms/accept", { version }),
  previewInvitation: (token: string) => request<InvitationPreview>("POST", "/api/v1/invitations/preview", { token }),
  acceptInvitation: (b: S["AcceptInvitationRequest"]) => request<S["AcceptInvitationResult"]>("POST", "/api/v1/invitations/accept", b),

  me: () => getJSON<User>("/api/v1/me"),
  updateMe: (b: S["UpdateMeRequest"]) => request<User>("PATCH", "/api/v1/me", b),
  changePassword: (b: S["ChangePasswordRequest"]) => request<void>("POST", "/api/v1/me/password", b),
  mySessions: () => getJSON<S["SessionList"]>("/api/v1/me/sessions"),
  revokeSession: (id: string) => request<void>("DELETE", `/api/v1/me/sessions/${encodeURIComponent(id)}`),
  recoveryCodes: () => getJSON<S["RecoveryCodesStatus"]>("/api/v1/me/recovery-codes"),
  regenerateRecoveryCodes: () => request<S["RecoveryCodes"]>("POST", "/api/v1/me/recovery-codes"),
  myInvitations: () => getJSON<S["MyInvitationList"]>("/api/v1/me/invitations"),
  acceptMyInvitation: (id: string) => request<S["AcceptInvitationResult"]>("POST", `/api/v1/me/invitations/${id}/accept`),

  myTokens: () => getJSON<S["APITokenList"]>("/api/v1/tokens"),
  createToken: (b: S["CreateTokenRequest"]) => request<CreatedToken>("POST", "/api/v1/tokens", b),
  revokeMyToken: (id: string) => request<void>("DELETE", `/api/v1/tokens/${id}`),
  orgTokens: (org: string) => getJSON<S["APITokenList"]>(`/api/v1/orgs/${org}/tokens`),
  revokeOrgToken: (org: string, id: string) => request<void>("DELETE", `/api/v1/orgs/${org}/tokens/${id}`),
  deviceRequest: (code: string) => getJSON<DeviceRequest>(`/api/v1/auth/device/requests/${encodeURIComponent(code)}`),
  approveDevice: (b: S["DeviceApproveRequest"]) => request<APIToken | undefined>("POST", "/api/v1/auth/device/approve", b),
  tokenSettings: () => getJSON<S["TokenSettings"]>("/api/v1/admin/settings/tokens"),
  putTokenSettings: (b: S["TokenSettings"]) => request<S["TokenSettings"]>("PUT", "/api/v1/admin/settings/tokens", b),

  orgs: () => getJSON<S["OrgList"]>("/api/v1/orgs"),
  createOrg: (name: string) => request<Org>("POST", "/api/v1/orgs", { name }),
  org: (id: string) => getJSON<Org>(`/api/v1/orgs/${id}`),
  updateOrg: (id: string, b: S["UpdateOrgRequest"]) => request<Org>("PATCH", `/api/v1/orgs/${id}`, b),
  orgMembers: (id: string) => getJSON<S["OrgMemberList"]>(`/api/v1/orgs/${id}/members`),
  inviteOrgMember: (id: string, b: S["InviteRequest"]) => request<InvitationCreated>("POST", `/api/v1/orgs/${id}/members`, b),
  setOrgRole: (id: string, user: string, role: OrgRole) => request<void>("PATCH", `/api/v1/orgs/${id}/members/${user}`, { role }),
  removeOrgMember: (id: string, user: string) => request<void>("DELETE", `/api/v1/orgs/${id}/members/${user}`),
  leaveOrg: (id: string) => request<void>("POST", `/api/v1/orgs/${id}/leave`),
  transferOwnership: (id: string, user_id: string) => request<void>("POST", `/api/v1/orgs/${id}/transfer-ownership`, { user_id }),
  orgInvitations: (id: string) => getJSON<S["InvitationList"]>(`/api/v1/orgs/${id}/invitations`),
  revokeOrgInvitation: (id: string, inv: string) => request<void>("DELETE", `/api/v1/orgs/${id}/invitations/${inv}`),
  orgAudit: (id: string, p: AuditQuery = {}) => getJSON<AuditList>(`/api/v1/orgs/${id}/audit${qs(p)}`),
  orgQuotas: (id: string) => getJSON<OrgQuotas>(`/api/v1/orgs/${id}/quotas`),
  orgUsage: (id: string, p: { from?: string; to?: string; metric?: string } = {}) => getJSON<UsageReport>(`/api/v1/orgs/${id}/usage${qs(p)}`),
  orgUsageCsvUrl: (id: string, p: { from?: string; to?: string } = {}) => `/api/v1/orgs/${id}/usage${qs({ ...p, format: "csv" })}`,
  orgDedicatedRequests: (id: string) => getJSON<S["DedicatedRequestList"]>(`/api/v1/orgs/${id}/dedicated-requests`),
  deleteOrg: (id: string, b: S["DeleteOrgRequest"]) => request<S["OrgDeletion"]>("DELETE", `/api/v1/orgs/${id}`, b),
  cancelOrgDeletion: (id: string) => request<void>("POST", `/api/v1/orgs/${id}/cancel-deletion`),
  endBreakGlass: (id: string, session: string) => request<void>("POST", `/api/v1/orgs/${id}/break-glass/${session}/end`),

  projectMembers: (id: string) => getJSON<S["ProjectMemberList"]>(`/api/v1/projects/${id}/members`),
  addProjectMember: (id: string, b: S["ProjectMemberRequest"]) => request<S["ProjectMemberAdded"]>("POST", `/api/v1/projects/${id}/members`, b),
  setProjectRole: (id: string, user: string, role: ProjectRole) => request<void>("PATCH", `/api/v1/projects/${id}/members/${user}`, { role }),
  removeProjectMember: (id: string, user: string) => request<void>("DELETE", `/api/v1/projects/${id}/members/${user}`),
  myCredentials: (id: string) => getJSON<PersonalCredentialsInfo>(`/api/v1/projects/${id}/credentials`),
  issueMyCredentials: (id: string) => request<PersonalCredentials>("POST", `/api/v1/projects/${id}/credentials`),
  transferProject: (id: string, org_id: string) => request<Project>("POST", `/api/v1/projects/${id}/transfer`, { org_id }),
  projectAudit: (id: string, p: AuditQuery = {}) => getJSON<AuditList>(`/api/v1/projects/${id}/audit${qs(p)}`),
  projectStorage: (id: string) => getJSON<ProjectStorage>(`/api/v1/projects/${id}/storage`),
  reclaimSpace: (id: string, schema: string, table: string) => request<Operation>("POST", `/api/v1/projects/${id}/reclaim-space`, { schema, table }),
  reapedSessions: (id: string) => getJSON<S["ReapedSessionList"]>(`/api/v1/projects/${id}/reaped`),
  switchCredentials: (id: string, grace_days: number) => request<SwitchedCredentials>("POST", `/api/v1/projects/${id}/switch-credentials`, { grace_days }),

  users: (p: { q?: string; pending?: boolean } = {}) => getJSON<S["UserList"]>(`/api/v1/admin/users${qs({ q: p.q, pending: p.pending ? "true" : undefined })}`),
  updateUser: (id: string, b: S["UpdateUserRequest"]) => request<AdminUser>("PATCH", `/api/v1/admin/users/${id}`, b),
  resetUserTotp: (id: string) => request<void>("POST", `/api/v1/admin/users/${id}/reset-2fa`),
  platformInvitations: () => getJSON<S["InvitationList"]>("/api/v1/admin/invitations"),
  invitePlatform: (email: string) => request<InvitationCreated>("POST", "/api/v1/admin/invitations", { email }),
  revokePlatformInvitation: (id: string) => request<void>("DELETE", `/api/v1/admin/invitations/${id}`),
  signupSettings: () => getJSON<SignupSettings>("/api/v1/admin/settings/signup"),
  saveSignupSettings: (b: SignupSettings) => request<SignupSettings>("PUT", "/api/v1/admin/settings/signup", b),
  mailSettings: () => getJSON<MailSettings>("/api/v1/admin/settings/mail"),
  saveMailSettings: (b: MailSettingsRequest) => request<MailSettings>("PUT", "/api/v1/admin/settings/mail", b),
  publishTerms: (b: S["PublishTermsRequest"]) => request<Terms>("POST", "/api/v1/admin/settings/terms", b),
  platformAudit: (p: AuditQuery = {}) => getJSON<AuditList>(`/api/v1/admin/audit${qs(p)}`),
  adminOrgs: (q?: string) => getJSON<S["AdminOrgList"]>(`/api/v1/admin/orgs${qs({ q })}`),
  adminOrg: (id: string) => getJSON<AdminOrg>(`/api/v1/admin/orgs/${id}`),
  adminUpdateOrg: (id: string, b: S["AdminUpdateOrgRequest"]) => request<AdminOrg>("PATCH", `/api/v1/admin/orgs/${id}`, b),
  suspendOrg: (id: string, reason: string) => request<void>("POST", `/api/v1/admin/orgs/${id}/suspend`, { reason }),
  reinstateOrg: (id: string) => request<void>("POST", `/api/v1/admin/orgs/${id}/reinstate`),
  setOrgCluster: (id: string, instance_id: string, reserved: boolean) => request<AdminOrg>("POST", `/api/v1/admin/orgs/${id}/cluster`, { instance_id, reserved }),
  startBreakGlass: (id: string, reason: string, duration_minutes: number) =>
    request<BreakGlassSession>("POST", `/api/v1/admin/orgs/${id}/break-glass`, { reason, duration_minutes }),
  plans: () => getJSON<S["PlanList"]>("/api/v1/admin/plans"),
  createPlan: (b: S["PlanRequest"]) => request<Plan>("POST", "/api/v1/admin/plans", b),
  updatePlan: (id: string, b: S["PlanRequest"]) => request<Plan>("PATCH", `/api/v1/admin/plans/${id}`, b),
  dedicatedRequests: (status?: string) => getJSON<S["DedicatedRequestList"]>(`/api/v1/admin/dedicated-requests${qs({ status })}`),
  approveDedicatedRequest: (id: string, b: S["DecideRequest"]) => request<Operation>("POST", `/api/v1/admin/dedicated-requests/${id}/approve`, b),
  rejectDedicatedRequest: (id: string, b: S["DecideRequest"]) => request<void>("POST", `/api/v1/admin/dedicated-requests/${id}/reject`, b),
  platformUsage: (p: { from?: string; to?: string } = {}) => getJSON<PlatformUsage>(`/api/v1/admin/usage${qs(p)}`),
  sharedClusters: () => getJSON<S["SharedClusterList"]>("/api/v1/admin/shared-clusters"),

  projects: (org?: string, status?: string) => getJSON<S["ProjectList"]>(`/api/v1/projects${qs({ org, status, limit: 500 })}`),
  project: (id: string) => getJSON<Project>(`/api/v1/projects/${id}`),
  createProject: (b: CreateProjectRequest) => request<ProjectCredentials>("POST", "/api/v1/projects", b),
  profiles: () => getJSON<ProfileList>("/api/v1/profiles"),
  promotionEstimate: (id: string) => getJSON<S["PromotionEstimate"]>(`/api/v1/projects/${id}/promote`),
  /** An operation, or a dedicated request when beyond the org's allowance (V2 §10.6). */
  promote: (id: string, b: S["PromoteRequest"]) => request<Operation | DedicatedRequest>("POST", `/api/v1/projects/${id}/promote`, b),
  demotePreflight: (id: string, b: DemoteRequest) => request<DemotePreflight>("POST", `/api/v1/projects/${id}/demote/preflight`, b),
  demote: (id: string, b: DemoteRequest) => request<Operation>("POST", `/api/v1/projects/${id}/demote`, b),
  sql: (id: string, b: S["SqlRequest"]) => request<SqlResult>("POST", `/api/v1/projects/${id}/sql`, b),
  cancelSql: (id: string, query_id: string) => request<S["SqlCancelResult"]>("POST", `/api/v1/projects/${id}/sql/cancel`, { query_id }),
  schema: (id: string) => getJSON<DbSchema>(`/api/v1/projects/${id}/schema`),
  tableRows: (id: string, schema: string, table: string, after?: string, grid: GridOptions = {}) =>
    getJSON<TablePage>(`${tableBase(id, schema, table)}/rows${gridQs(grid, { after })}`),
  tableInfo: (id: string, schema: string, table: string) => getJSON<TableInfo>(tableBase(id, schema, table)),
  exportUrl: (id: string, schema: string, table: string, format: "csv" | "json", grid: GridOptions = {}) =>
    `${tableBase(id, schema, table)}/export${gridQs(grid, { format })}`,
  saveRows: (id: string, schema: string, table: string, changes: RowChange[]) =>
    request<SaveRowsResult>("POST", `${tableBase(id, schema, table)}/changes`, { changes }),
  previewSchema: (id: string, change: SchemaChange) => request<SchemaPlan>("POST", `/api/v1/projects/${id}/schema/preview`, { change }),
  applySchema: (id: string, change: SchemaChange, hash: string, confirm?: string) =>
    request<S["SchemaApplied"]>("POST", `/api/v1/projects/${id}/schema/apply`, { change, hash, confirm }),
  schemaMigration: (id: string, change: SchemaChange, format: MigrationFormat) =>
    request<S["SchemaMigration"]>("POST", `/api/v1/projects/${id}/schema/migration`, { change, format }),
  editorPreferences: (id: string) => getJSON<S["EditorPreferences"]>(`/api/v1/projects/${id}/editor-preferences`),
  extensions: (id: string) => getJSON<S["ExtensionList"]>(`/api/v1/projects/${id}/extensions`),
  enableExtension: (id: string, name: string) => request<S["ExtensionList"]>("POST", `/api/v1/projects/${id}/extensions`, { name }),
  projectMetrics: (id: string, range: MetricRange) => getJSON<MetricsResponse>(`/api/v1/projects/${id}/metrics${qs({ range })}`),
  nodeMetrics: (id: string, range: MetricRange) => getJSON<MetricsResponse>(`/api/v1/nodes/${id}/metrics${qs({ range })}`),
  pitr: (id: string, b: S["PitrRequest"]) => request<ProjectCredentials>("POST", `/api/v1/projects/${id}/pitr`, b),
  instanceAction: (id: string, action: "start" | "stop" | "restart") =>
    request<S["InstanceState"]>("POST", `/api/v1/projects/${id}/instance`, { action }),
  updateProject: (id: string, b: S["UpdateProjectRequest"]) =>
    request<ProjectUpdated>("PATCH", `/api/v1/projects/${id}/settings`, b),
  rotatePassword: (id: string) => request<ProjectCredentials>("POST", `/api/v1/projects/${id}/rotate-password`),
  branches: (id: string) => getJSON<S["ProjectList"]>(`/api/v1/projects/${id}/branches`),
  webhooks: (id: string) => getJSON<S["WebhookList"]>(`/api/v1/projects/${id}/webhooks`),
  createWebhook: (id: string, b: WebhookRequest) => request<S["WebhookCreated"]>("POST", `/api/v1/projects/${id}/webhooks`, b),
  updateWebhook: (id: string, wid: string, b: S["WebhookUpdate"]) => request<Webhook>("PATCH", `/api/v1/projects/${id}/webhooks/${wid}`, b),
  deleteWebhook: (id: string, wid: string) => request<void>("DELETE", `/api/v1/projects/${id}/webhooks/${wid}`),
  testWebhook: (id: string, wid: string) => request<S["WebhookTestResult"]>("POST", `/api/v1/projects/${id}/webhooks/${wid}/test`),
  rotateWebhookSecret: (id: string, wid: string) => request<S["WebhookSecret"]>("POST", `/api/v1/projects/${id}/webhooks/${wid}/rotate-secret`),
  webhookDeliveries: (id: string, wid: string, dead = false) =>
    getJSON<S["WebhookDeliveryList"]>(`/api/v1/projects/${id}/webhooks/${wid}/deliveries?limit=100${dead ? "&dead=true" : ""}`),
  replayWebhook: (id: string, wid: string, b: S["ReplayRequest"]) => request<S["ReplayResult"]>("POST", `/api/v1/projects/${id}/webhooks/${wid}/replay`, b),
  jobs: (id: string) => getJSON<S["JobList"]>(`/api/v1/projects/${id}/jobs`),
  createJob: (id: string, b: JobRequest) => request<S["JobCreated"]>("POST", `/api/v1/projects/${id}/jobs`, b),
  updateJob: (id: string, jid: string, b: S["JobUpdate"]) => request<Job>("PATCH", `/api/v1/projects/${id}/jobs/${jid}`, b),
  deleteJob: (id: string, jid: string) => request<void>("DELETE", `/api/v1/projects/${id}/jobs/${jid}`),
  runJob: (id: string, jid: string) => request<JobRun>("POST", `/api/v1/projects/${id}/jobs/${jid}/run`),
  jobRuns: (id: string, jid: string) => getJSON<S["JobRunList"]>(`/api/v1/projects/${id}/jobs/${jid}/runs?limit=50`),
  orgOutbound: (org: string) => getJSON<S["OrgOutbound"]>(`/api/v1/admin/orgs/${org}/outbound`),
  setOrgOutbound: (org: string, hosts: string[]) => request<S["OrgOutbound"]>("PUT", `/api/v1/admin/orgs/${org}/outbound`, { hosts }),
  createBranch: (id: string, b: S["BranchRequest"]) => request<ProjectCredentials>("POST", `/api/v1/projects/${id}/branches`, b),
  resetBranch: (id: string, b: S["BranchResetRequest"] = {}) => request<Operation>("POST", `/api/v1/projects/${id}/reset`, b),
  detachBranch: (id: string) => request<Project>("POST", `/api/v1/projects/${id}/detach`),
  deleteProject: (id: string, confirm: string, skipFinalBackup = false) =>
    request<Operation>("DELETE", `/api/v1/projects/${id}${qs({ confirm, skip_final_backup: skipFinalBackup ? "true" : undefined })}`),

  backups: (p: { org?: string; project_id?: string; kind?: string; limit?: number } = {}) => getJSON<S["BackupList"]>(`/api/v1/backups${qs(p)}`),
  backupOverview: (org?: string) => getJSON<BackupOverview>(`/api/v1/backups/overview${qs({ org })}`),
  backupNow: (projectId: string) => request<Operation>("POST", `/api/v1/projects/${projectId}/backups`),
  restore: (backupId: string, b: S["RestoreRequest"]) => request<RestoreResponse>("POST", `/api/v1/backups/${backupId}/restore`, b),
  restoreTest: (projectId?: string) => request<Operation>("POST", `/api/v1/restore-tests${qs({ project_id: projectId })}`),

  storage: () => getJSON<StorageSettings>("/api/v1/settings/storage"),
  saveStorage: (b: StorageRequest) => request<StorageTestResult>("PUT", "/api/v1/settings/storage", b),
  testStorage: (b: StorageRequest) => request<StorageTestResult>("POST", "/api/v1/settings/storage/test", b),
  backupKey: () => getJSON<BackupKeyInfo>("/api/v1/settings/backup-key"),
  generateBackupKey: () => request<BackupKeyExport>("POST", "/api/v1/settings/backup-key"),
  exportBackupKey: () => request<BackupKeyExport>("POST", "/api/v1/settings/backup-key/export"),
  confirmBackupKey: (key: string) => request<BackupKeyInfo>("POST", "/api/v1/settings/backup-key/confirm", { key }),

  // Storage targets (V2 §6). org undefined: platform targets (platform admin).
  storageTargets: (org?: string) => getJSON<S["StorageTargetList"]>(org ? `/api/v1/orgs/${org}/storage-targets` : "/api/v1/admin/storage-targets"),
  createStorageTarget: (org: string | undefined, b: S["StorageTargetRequest"]) =>
    request<S["StorageTargetSaveResult"]>("POST", org ? `/api/v1/orgs/${org}/storage-targets` : "/api/v1/admin/storage-targets", b),
  updateStorageTarget: (org: string | undefined, id: string, b: S["StorageTargetRequest"]) =>
    request<S["StorageTargetSaveResult"]>("PATCH", org ? `/api/v1/orgs/${org}/storage-targets/${id}` : `/api/v1/admin/storage-targets/${id}`, b),
  deleteStorageTarget: (org: string | undefined, id: string, acceptUnrestorable = false) =>
    request<void>(
      "DELETE",
      `${org ? `/api/v1/orgs/${org}/storage-targets/${id}` : `/api/v1/admin/storage-targets/${id}`}${qs({ accept_unrestorable: acceptUnrestorable ? "true" : undefined })}`,
    ),
  testStorageTarget: (org: string | undefined, b: S["StorageTargetTestRequest"]) =>
    request<StorageTestResult>("POST", `/api/v1/storage-targets/test${qs({ org })}`, b),
  projectBackupStorage: (id: string) => getJSON<S["ProjectBackupStorage"]>(`/api/v1/projects/${id}/storage-target`),
  setProjectStorageTarget: (id: string, b: S["ProjectStorageTargetRequest"]) =>
    request<S["ProjectStorageTargetResult"]>("PUT", `/api/v1/projects/${id}/storage-target`, b),
  enableProjectBackupKey: (id: string, rotate = false) => request<S["ProjectBackupKey"]>("POST", `/api/v1/projects/${id}/backup-key`, { rotate }),
  /** The key file (README + armored OpenPGP key); needs a recent re-authentication. */
  downloadProjectBackupKey: async (id: string): Promise<string> => {
    const res = await fetch(`/api/v1/projects/${id}/backup-key/download`, { credentials: "same-origin" });
    if (!res.ok) {
      let err: ApiErrorBody | undefined;
      try {
        err = (await res.json()) as ApiErrorBody;
      } catch {
        err = undefined;
      }
      throw new ApiRequestError(res.status, err);
    }
    return res.text();
  },

  importPreflight: (source_url: string) => request<ImportPreflight>("POST", "/api/v1/imports/preflight", { source_url }),
  createImport: (b: S["ImportRequest"]) => request<ProjectCredentials>("POST", "/api/v1/imports", b),

  nodes: () => getJSON<S["NodeList"]>("/api/v1/nodes"),
  nodeToken: (id: string) => request<RegistrationToken>("POST", `/api/v1/nodes/${id}/registration-token`),
  node: (id: string) => getJSON<NodeDetail>(`/api/v1/nodes/${id}`),
  createNode: (b: S["CreateNodeRequest"]) => request<NodeCreated>("POST", "/api/v1/nodes", b),
  removeNode: (id: string) => request<void>("DELETE", `/api/v1/nodes/${id}`),
  updateNode: (id: string, role: "shared" | "dedicated" | "both") => request<Node>("PATCH", `/api/v1/nodes/${id}`, { role }),
  createSharedCluster: (id: string, memory_mb: number) => request<Operation>("POST", `/api/v1/nodes/${id}/shared-cluster`, { memory_mb }),

  operations: (p: { org?: string; platform?: string; project_id?: string; status?: string; kind?: string; limit?: number } = {}) =>
    getJSON<S["OperationList"]>(`/api/v1/operations${qs(p)}`),
  operation: (id: string) => getJSON<Operation>(`/api/v1/operations/${id}`),


  alerts: (status?: "firing" | "resolved") => getJSON<S["AlertList"]>(`/api/v1/alerts${qs({ status, limit: 200 })}`),
  alertSettings: () => getJSON<AlertSettings>("/api/v1/settings/alerts"),
  saveAlertSettings: (b: AlertSettingsRequest) => request<AlertSettings>("PUT", "/api/v1/settings/alerts", b),
  testAlerts: () => request<S["AlertTestResult"]>("POST", "/api/v1/settings/alerts/test"),
  isolationChecks: () => getJSON<S["IsolationCheckList"]>("/api/v1/security/isolation-checks"),
  runIsolationChecks: () => request<S["OperationList"]>("POST", "/api/v1/security/isolation-checks"),

  generalSettings: () => getJSON<GeneralSettings>("/api/v1/settings/general"),
  setDbHost: (db_host: string) => request<GeneralSettings>("PUT", "/api/v1/settings/db-host", { db_host }),
  checkDbHost: (db_host: string) => request<DnsCheck>("POST", "/api/v1/settings/db-host/check", { db_host }),
};

/** Turns any thrown value into a message fit for the UI. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiRequestError) return err.message;
  if (err instanceof Error) return err.message;
  return String(err);
}
