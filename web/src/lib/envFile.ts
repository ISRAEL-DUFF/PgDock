import type { ProjectCredentials } from "../api/client";

/** The contents of a `.env` file holding a project's connection strings. */
export function envFileContents(creds: ProjectCredentials): string {
  const { project, connection } = creds;
  return [
    `# PGDock project: ${project.name} (${project.db_name})`,
    "# Contains a password. Keep it out of version control.",
    `DATABASE_URL="${connection.pooled_url}"`,
    `DATABASE_SESSION_URL="${connection.session_url}"`,
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
