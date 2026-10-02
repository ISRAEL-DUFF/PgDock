import type { components } from "./schema";

type S = components["schemas"];
export type Version = S["Version"];
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
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
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

  projectMembers: (id: string) => getJSON<S["ProjectMemberList"]>(`/api/v1/projects/${id}/members`),
  addProjectMember: (id: string, b: S["ProjectMemberRequest"]) => request<S["ProjectMemberAdded"]>("POST", `/api/v1/projects/${id}/members`, b),
  setProjectRole: (id: string, user: string, role: ProjectRole) => request<void>("PATCH", `/api/v1/projects/${id}/members/${user}`, { role }),
  removeProjectMember: (id: string, user: string) => request<void>("DELETE", `/api/v1/projects/${id}/members/${user}`),
  myCredentials: (id: string) => getJSON<PersonalCredentialsInfo>(`/api/v1/projects/${id}/credentials`),
  issueMyCredentials: (id: string) => request<PersonalCredentials>("POST", `/api/v1/projects/${id}/credentials`),
  transferProject: (id: string, org_id: string) => request<Project>("POST", `/api/v1/projects/${id}/transfer`, { org_id }),
  projectAudit: (id: string, p: AuditQuery = {}) => getJSON<AuditList>(`/api/v1/projects/${id}/audit${qs(p)}`),

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

  projects: (org?: string, status?: string) => getJSON<S["ProjectList"]>(`/api/v1/projects${qs({ org, status, limit: 500 })}`),
  project: (id: string) => getJSON<Project>(`/api/v1/projects/${id}`),
  createProject: (b: CreateProjectRequest) => request<ProjectCredentials>("POST", "/api/v1/projects", b),
  profiles: () => getJSON<ProfileList>("/api/v1/profiles"),
  promotionEstimate: (id: string) => getJSON<S["PromotionEstimate"]>(`/api/v1/projects/${id}/promote`),
  promote: (id: string, b: S["PromoteRequest"]) => request<Operation>("POST", `/api/v1/projects/${id}/promote`, b),
  sql: (id: string, b: S["SqlRequest"]) => request<SqlResult>("POST", `/api/v1/projects/${id}/sql`, b),
  cancelSql: (id: string, query_id: string) => request<S["SqlCancelResult"]>("POST", `/api/v1/projects/${id}/sql/cancel`, { query_id }),
  schema: (id: string) => getJSON<DbSchema>(`/api/v1/projects/${id}/schema`),
  tableRows: (id: string, schema: string, table: string, after?: string) =>
    getJSON<TablePage>(`/api/v1/projects/${id}/tables/${encodeURIComponent(schema)}/${encodeURIComponent(table)}/rows${qs({ after })}`),
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
