package api

import "github.com/israel-duff/pgdock/internal/authz"

// scope says how a route's resource is found before authz.Can runs.
type scope int

const (
	scopePublic    scope = iota // no session needed
	scopeSelf                   // a signed-in user acting on their own account
	scopePlatform               // platform admin only
	scopeOrgPath                // {org} in the path
	scopeOrgQuery               // ?org=, the user's personal organisation by default
	scopeProject                // {id} is a project
	scopeBackup                 // {id} is a backup; its project's organisation
	scopeOperation              // {id} is an operation; its project's organisation
	scopeBody                   // the organisation is in the request body; the handler checks
)

// rule is a route's authorization: where its resource is and what action
// it performs (V2 §2.6: every route declares its action).
type rule struct {
	scope  scope
	action authz.Action
	// beforeTerms routes work while the user still has to accept new terms.
	beforeTerms bool
	// branchAction, when set, replaces action on a project that is a
	// branch (V2 §2.3: developers delete branches, not projects).
	branchAction authz.Action
	// token is the scope an API token needs on a scopeSelf route; empty
	// means the route is for browser sessions only. Other scopes are
	// checked by authz.Can (V2 §7.2).
	token string
}

// routeRules covers every route in api/openapi.yaml; TestEveryRouteDeclaresAnAction
// fails when one is missing, and the guard refuses undeclared routes.
var routeRules = map[string]rule{
	// Public.
	"GET /healthz":                             {scope: scopePublic},
	"GET /readyz":                              {scope: scopePublic},
	"GET /api/v1/version":                      {scope: scopePublic},
	"GET /api/v1/session":                      {scope: scopePublic, beforeTerms: true},
	"POST /api/v1/auth/login":                  {scope: scopePublic, beforeTerms: true},
	"POST /api/v1/auth/totp":                   {scope: scopePublic, beforeTerms: true},
	"POST /api/v1/setup/begin":                 {scope: scopePublic},
	"POST /api/v1/setup/complete":              {scope: scopePublic},
	"POST /api/v1/agent/register":              {scope: scopePublic},
	"POST /api/v1/auth/signup":                 {scope: scopePublic},
	"POST /api/v1/auth/verify-email":           {scope: scopePublic, beforeTerms: true},
	"POST /api/v1/auth/verify-email/resend":    {scope: scopePublic, beforeTerms: true},
	"POST /api/v1/auth/password-reset":         {scope: scopePublic, beforeTerms: true},
	"POST /api/v1/auth/password-reset/confirm": {scope: scopePublic, beforeTerms: true},
	"GET /api/v1/terms":                        {scope: scopePublic, beforeTerms: true},
	"POST /api/v1/invitations/preview":         {scope: scopePublic},
	"POST /api/v1/invitations/accept":          {scope: scopePublic},
	// The CLI's device login; the approval itself needs a session.
	"POST /api/v1/auth/device":       {scope: scopePublic},
	"POST /api/v1/auth/device/token": {scope: scopePublic},
	// /metrics takes a bearer token instead; the handler checks it or the session.
	"GET /metrics": {scope: scopePublic},

	// The signed-in user.
	"POST /api/v1/auth/reauth":                           {scope: scopeSelf, action: authz.Self, beforeTerms: true},
	"POST /api/v1/auth/logout":                           {scope: scopeSelf, action: authz.Self, beforeTerms: true},
	"GET /api/v1/me":                                     {scope: scopeSelf, action: authz.Self, beforeTerms: true, token: authz.ScopeRead},
	"PATCH /api/v1/me":                                   {scope: scopeSelf, action: authz.Self},
	"POST /api/v1/me/password":                           {scope: scopeSelf, action: authz.Self},
	"GET /api/v1/me/sessions":                            {scope: scopeSelf, action: authz.Self},
	"DELETE /api/v1/me/sessions/{session_id}":            {scope: scopeSelf, action: authz.Self},
	"GET /api/v1/me/recovery-codes":                      {scope: scopeSelf, action: authz.Self},
	"POST /api/v1/me/recovery-codes":                     {scope: scopeSelf, action: authz.Self},
	"POST /api/v1/me/terms/accept":                       {scope: scopeSelf, action: authz.Self, beforeTerms: true},
	"GET /api/v1/me/invitations":                         {scope: scopeSelf, action: authz.Self},
	"POST /api/v1/me/invitations/{invitation_id}/accept": {scope: scopeSelf, action: authz.Self},
	"GET /api/v1/orgs":                                   {scope: scopeSelf, action: authz.Self, token: authz.ScopeRead},
	"POST /api/v1/orgs":                                  {scope: scopeSelf, action: authz.Self},
	"GET /api/v1/settings/general":                       {scope: scopeSelf, action: authz.Self, token: authz.ScopeRead},
	"GET /api/v1/profiles":                               {scope: scopeSelf, action: authz.Self, token: authz.ScopeRead},
	"POST /api/v1/imports/preflight":                     {scope: scopeSelf, action: authz.Self},
	// API tokens (V2 §7.2): a token may manage its user's tokens in its own
	// organisation, never exceeding itself (the handlers check).
	"GET /api/v1/tokens":                           {scope: scopeSelf, action: authz.Self, token: authz.ScopeRead},
	"POST /api/v1/tokens":                          {scope: scopeSelf, action: authz.Self, token: authz.ScopeWrite},
	"DELETE /api/v1/tokens/{token_id}":             {scope: scopeSelf, action: authz.Self, token: authz.ScopeRead},
	"GET /api/v1/auth/device/requests/{user_code}": {scope: scopeSelf, action: authz.Self},
	"POST /api/v1/auth/device/approve":             {scope: scopeSelf, action: authz.Self},

	// Organisations.
	"GET /api/v1/orgs/{org}":                                {scope: scopeOrgPath, action: authz.OrgView},
	"PATCH /api/v1/orgs/{org}":                              {scope: scopeOrgPath, action: authz.OrgManage},
	"GET /api/v1/orgs/{org}/members":                        {scope: scopeOrgPath, action: authz.OrgView},
	"POST /api/v1/orgs/{org}/members":                       {scope: scopeOrgPath, action: authz.OrgManage},
	"PATCH /api/v1/orgs/{org}/members/{user}":               {scope: scopeOrgPath, action: authz.OrgManage},
	"DELETE /api/v1/orgs/{org}/members/{user}":              {scope: scopeOrgPath, action: authz.OrgManage},
	"POST /api/v1/orgs/{org}/leave":                         {scope: scopeOrgPath, action: authz.OrgView},
	"POST /api/v1/orgs/{org}/transfer-ownership":            {scope: scopeOrgPath, action: authz.OrgOwnerOnly},
	"GET /api/v1/orgs/{org}/invitations":                    {scope: scopeOrgPath, action: authz.OrgManage},
	"DELETE /api/v1/orgs/{org}/invitations/{invitation_id}": {scope: scopeOrgPath, action: authz.OrgManage},
	"GET /api/v1/orgs/{org}/audit":                          {scope: scopeOrgPath, action: authz.OrgAudit},
	"DELETE /api/v1/orgs/{org}":                             {scope: scopeOrgPath, action: authz.OrgOwnerOnly},
	"POST /api/v1/orgs/{org}/cancel-deletion":               {scope: scopeOrgPath, action: authz.OrgOwnerOnly},
	"POST /api/v1/orgs/{org}/break-glass/{session_id}/end":  {scope: scopeOrgPath, action: authz.OrgOwnerOnly},
	"GET /api/v1/orgs/{org}/quotas":                         {scope: scopeOrgPath, action: authz.OrgView},
	"GET /api/v1/orgs/{org}/usage":                          {scope: scopeOrgPath, action: authz.OrgAudit},
	"GET /api/v1/orgs/{org}/dedicated-requests":             {scope: scopeOrgPath, action: authz.OrgAudit},
	"GET /api/v1/orgs/{org}/tokens":                         {scope: scopeOrgPath, action: authz.OrgManage},
	"DELETE /api/v1/orgs/{org}/tokens/{token_id}":           {scope: scopeOrgPath, action: authz.OrgManage},
	// Org storage targets (V2 §6): owners and admins.
	"GET /api/v1/orgs/{org}/storage-targets":                {scope: scopeOrgPath, action: authz.OrgManage},
	"POST /api/v1/orgs/{org}/storage-targets":               {scope: scopeOrgPath, action: authz.OrgManage},
	"GET /api/v1/orgs/{org}/storage-targets/{target_id}":    {scope: scopeOrgPath, action: authz.OrgManage},
	"PATCH /api/v1/orgs/{org}/storage-targets/{target_id}":  {scope: scopeOrgPath, action: authz.OrgManage},
	"DELETE /api/v1/orgs/{org}/storage-targets/{target_id}": {scope: scopeOrgPath, action: authz.OrgManage},
	"POST /api/v1/storage-targets/test":                     {scope: scopeOrgQuery, action: authz.OrgManage},
	"GET /api/v1/projects":                                  {scope: scopeOrgQuery, action: authz.OrgView},
	"GET /api/v1/operations":                                {scope: scopeOrgQuery, action: authz.OrgView},
	"GET /api/v1/backups":                                   {scope: scopeOrgQuery, action: authz.OrgView},
	"GET /api/v1/backups/overview":                          {scope: scopeOrgQuery, action: authz.OrgView},
	"POST /api/v1/projects":                                 {scope: scopeBody, action: authz.OrgCreateProject},
	"POST /api/v1/imports":                                  {scope: scopeBody, action: authz.OrgCreateProject},

	// Projects.
	"GET /api/v1/projects/{id}":                                  {scope: scopeProject, action: authz.ProjectView},
	"DELETE /api/v1/projects/{id}":                               {scope: scopeProject, action: authz.ProjectDelete, branchAction: authz.BranchManage},
	"PATCH /api/v1/projects/{id}/settings":                       {scope: scopeProject, action: authz.ProjectSettings},
	"POST /api/v1/projects/{id}/rotate-password":                 {scope: scopeProject, action: authz.ProjectSettings},
	"POST /api/v1/projects/{id}/backups":                         {scope: scopeProject, action: authz.BackupCreate},
	"POST /api/v1/projects/{id}/pitr":                            {scope: scopeProject, action: authz.BackupCreate},
	"GET /api/v1/projects/{id}/promote":                          {scope: scopeProject, action: authz.ProjectPromote},
	"POST /api/v1/projects/{id}/promote":                         {scope: scopeProject, action: authz.ProjectPromote},
	"POST /api/v1/projects/{id}/demote/preflight":                {scope: scopeProject, action: authz.ProjectPromote},
	"POST /api/v1/projects/{id}/demote":                          {scope: scopeProject, action: authz.ProjectPromote},
	"POST /api/v1/projects/{id}/instance":                        {scope: scopeProject, action: authz.ProjectSettings},
	"POST /api/v1/projects/{id}/sql":                             {scope: scopeProject, action: authz.ConsoleRead},
	"POST /api/v1/projects/{id}/sql/cancel":                      {scope: scopeProject, action: authz.ConsoleRead},
	"GET /api/v1/projects/{id}/schema":                           {scope: scopeProject, action: authz.ConsoleRead},
	"GET /api/v1/projects/{id}/tables/{schema}/{table}/rows":     {scope: scopeProject, action: authz.ConsoleRead},
	"GET /api/v1/projects/{id}/tables/{schema}/{table}":          {scope: scopeProject, action: authz.ConsoleRead},
	"GET /api/v1/projects/{id}/tables/{schema}/{table}/export":   {scope: scopeProject, action: authz.ConsoleRead},
	"POST /api/v1/projects/{id}/tables/{schema}/{table}/changes": {scope: scopeProject, action: authz.TableEdit},
	"POST /api/v1/projects/{id}/schema/preview":                  {scope: scopeProject, action: authz.ConsoleRead},
	"POST /api/v1/projects/{id}/schema/migration":                {scope: scopeProject, action: authz.ConsoleRead},
	"POST /api/v1/projects/{id}/schema/apply":                    {scope: scopeProject, action: authz.TableEdit},
	"GET /api/v1/projects/{id}/editor-preferences":               {scope: scopeProject, action: authz.ConsoleRead},
	"GET /api/v1/projects/{id}/extensions":                       {scope: scopeProject, action: authz.ProjectView},
	"POST /api/v1/projects/{id}/extensions":                      {scope: scopeProject, action: authz.ProjectSettings},
	"GET /api/v1/projects/{id}/metrics":                          {scope: scopeProject, action: authz.ProjectView},
	"GET /api/v1/projects/{id}/members":                          {scope: scopeProject, action: authz.ProjectView},
	"POST /api/v1/projects/{id}/members":                         {scope: scopeProject, action: authz.ProjectMembers},
	"PATCH /api/v1/projects/{id}/members/{user}":                 {scope: scopeProject, action: authz.ProjectMembers},
	"DELETE /api/v1/projects/{id}/members/{user}":                {scope: scopeProject, action: authz.ProjectMembers},
	"GET /api/v1/projects/{id}/credentials":                      {scope: scopeProject, action: authz.ProjectCredentials},
	"POST /api/v1/projects/{id}/credentials":                     {scope: scopeProject, action: authz.ProjectCredentials},
	"POST /api/v1/projects/{id}/transfer":                        {scope: scopeProject, action: authz.ProjectDelete},
	"GET /api/v1/projects/{id}/audit":                            {scope: scopeProject, action: authz.ProjectAudit},
	"POST /api/v1/projects/{id}/switch-credentials":              {scope: scopeProject, action: authz.ProjectSettings},
	"GET /api/v1/projects/{id}/storage":                          {scope: scopeProject, action: authz.ProjectView},
	"POST /api/v1/projects/{id}/reclaim-space":                   {scope: scopeProject, action: authz.ProjectSettings},
	"GET /api/v1/projects/{id}/reaped":                           {scope: scopeProject, action: authz.ProjectView},
	"GET /api/v1/projects/{id}/branches":                         {scope: scopeProject, action: authz.ProjectView},
	"POST /api/v1/projects/{id}/branches":                        {scope: scopeProject, action: authz.BranchManage},
	"POST /api/v1/projects/{id}/reset":                           {scope: scopeProject, action: authz.BranchManage},
	"POST /api/v1/projects/{id}/detach":                          {scope: scopeProject, action: authz.BranchManage},

	// Webhooks and scheduled jobs (V2 §9).
	"GET /api/v1/projects/{id}/webhooks":                             {scope: scopeProject, action: authz.AutomationManage},
	"POST /api/v1/projects/{id}/webhooks":                            {scope: scopeProject, action: authz.AutomationManage},
	"GET /api/v1/projects/{id}/webhooks/{webhook_id}":                {scope: scopeProject, action: authz.AutomationManage},
	"PATCH /api/v1/projects/{id}/webhooks/{webhook_id}":              {scope: scopeProject, action: authz.AutomationManage},
	"DELETE /api/v1/projects/{id}/webhooks/{webhook_id}":             {scope: scopeProject, action: authz.AutomationManage},
	"POST /api/v1/projects/{id}/webhooks/{webhook_id}/test":          {scope: scopeProject, action: authz.AutomationManage},
	"POST /api/v1/projects/{id}/webhooks/{webhook_id}/rotate-secret": {scope: scopeProject, action: authz.AutomationManage},
	"GET /api/v1/projects/{id}/webhooks/{webhook_id}/deliveries":     {scope: scopeProject, action: authz.AutomationManage},
	"POST /api/v1/projects/{id}/webhooks/{webhook_id}/replay":        {scope: scopeProject, action: authz.AutomationManage},
	"GET /api/v1/projects/{id}/jobs":                                 {scope: scopeProject, action: authz.AutomationManage},
	"POST /api/v1/projects/{id}/jobs":                                {scope: scopeProject, action: authz.AutomationManage},
	"GET /api/v1/projects/{id}/jobs/{job_id}":                        {scope: scopeProject, action: authz.AutomationManage},
	"PATCH /api/v1/projects/{id}/jobs/{job_id}":                      {scope: scopeProject, action: authz.AutomationManage},
	"DELETE /api/v1/projects/{id}/jobs/{job_id}":                     {scope: scopeProject, action: authz.AutomationManage},
	"POST /api/v1/projects/{id}/jobs/{job_id}/run":                   {scope: scopeProject, action: authz.AutomationManage},
	"GET /api/v1/projects/{id}/jobs/{job_id}/runs":                   {scope: scopeProject, action: authz.AutomationManage},
	"GET /api/v1/projects/{id}/storage-target":                       {scope: scopeProject, action: authz.ProjectView},
	"PUT /api/v1/projects/{id}/storage-target":                       {scope: scopeProject, action: authz.BackupStorage},
	"POST /api/v1/projects/{id}/backup-key":                          {scope: scopeProject, action: authz.BackupStorage},
	"GET /api/v1/projects/{id}/backup-key/download":                  {scope: scopeProject, action: authz.BackupStorage},
	// Restoring in place needs the project admin role; the handler checks
	// it for that mode.
	"POST /api/v1/backups/{id}/restore":  {scope: scopeBackup, action: authz.BackupCreate},
	"GET /api/v1/operations/{id}":        {scope: scopeOperation, action: authz.ProjectView},
	"GET /api/v1/operations/{id}/stream": {scope: scopeOperation, action: authz.ProjectView},

	// The platform.
	"PUT /api/v1/settings/db-host":                               {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/settings/db-host/check":                        {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/dev/operations":                                {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/restore-tests":                                 {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/settings/storage":                               {scope: scopePlatform, action: authz.PlatformManage},
	"PUT /api/v1/settings/storage":                               {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/settings/storage/test":                         {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/settings/backup-key":                            {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/settings/backup-key":                           {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/settings/backup-key/export":                    {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/settings/backup-key/confirm":                   {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/nodes":                                          {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/nodes":                                         {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/nodes/{id}":                                     {scope: scopePlatform, action: authz.PlatformManage},
	"PATCH /api/v1/nodes/{id}":                                   {scope: scopePlatform, action: authz.PlatformManage},
	"DELETE /api/v1/nodes/{id}":                                  {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/nodes/{id}/shared-cluster":                     {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/nodes/{id}/registration-token":                 {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/nodes/{id}/metrics":                             {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/security/isolation-checks":                      {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/security/isolation-checks":                     {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/alerts":                                         {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/settings/alerts":                                {scope: scopePlatform, action: authz.PlatformManage},
	"PUT /api/v1/settings/alerts":                                {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/settings/alerts/test":                          {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/audit":                                    {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/users":                                    {scope: scopePlatform, action: authz.PlatformManage},
	"PATCH /api/v1/admin/users/{user}":                           {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/users/{user}/reset-2fa":                  {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/invitations":                              {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/invitations":                             {scope: scopePlatform, action: authz.PlatformManage},
	"DELETE /api/v1/admin/invitations/{invitation_id}":           {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/settings/signup":                          {scope: scopePlatform, action: authz.PlatformManage},
	"PUT /api/v1/admin/settings/signup":                          {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/settings/mail":                            {scope: scopePlatform, action: authz.PlatformManage},
	"PUT /api/v1/admin/settings/mail":                            {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/settings/terms":                          {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/orgs":                                     {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/orgs/{org}":                               {scope: scopePlatform, action: authz.PlatformManage},
	"PATCH /api/v1/admin/orgs/{org}":                             {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/orgs/{org}/suspend":                      {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/orgs/{org}/reinstate":                    {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/orgs/{org}/cluster":                      {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/orgs/{org}/outbound":                      {scope: scopePlatform, action: authz.PlatformManage},
	"PUT /api/v1/admin/orgs/{org}/outbound":                      {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/orgs/{org}/break-glass":                  {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/plans":                                    {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/plans":                                   {scope: scopePlatform, action: authz.PlatformManage},
	"PATCH /api/v1/admin/plans/{plan_id}":                        {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/dedicated-requests":                       {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/dedicated-requests/{request_id}/approve": {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/dedicated-requests/{request_id}/reject":  {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/usage":                                    {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/shared-clusters":                          {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/settings/tokens":                          {scope: scopePlatform, action: authz.PlatformManage},
	"PUT /api/v1/admin/settings/tokens":                          {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/storage-targets":                          {scope: scopePlatform, action: authz.PlatformManage},
	"POST /api/v1/admin/storage-targets":                         {scope: scopePlatform, action: authz.PlatformManage},
	"GET /api/v1/admin/storage-targets/{target_id}":              {scope: scopePlatform, action: authz.PlatformManage},
	"PATCH /api/v1/admin/storage-targets/{target_id}":            {scope: scopePlatform, action: authz.PlatformManage},
	"DELETE /api/v1/admin/storage-targets/{target_id}":           {scope: scopePlatform, action: authz.PlatformManage},
}
