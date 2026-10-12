import type { ProjectCredentials } from "../api/client";

/** The contents of a `.env` file holding a project's connection strings. */
export function envFileContents(creds: ProjectCredentials): string {
  const { project, connection, api } = creds;
  return [
    `# PGDock project: ${project.name} (${project.db_name})`,
    "# Contains a password. Keep it out of version control.",
    `DATABASE_URL="${connection.pooled_url}"`,
    `DATABASE_SESSION_URL="${connection.session_url}"`,
    // A branch of a project with backend services has its own API (V4.1 §9.5).
    ...(api
      ? [
          ...(api.url ? [`PGDOCK_API_URL="${api.url}"`] : []),
          `PGDOCK_PUBLISHABLE_KEY="${api.publishable_key}"`,
          `PGDOCK_SECRET_KEY="${api.secret_key}"`,
        ]
      : []),
    "",
  ].join("\n");
}

/** Saves text as a file through the browser's download mechanism. */
export function downloadTextFile(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}
