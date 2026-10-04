import { Link } from "@tanstack/react-router";
import { AuthShell } from "../components/Layout";

export function NotFound() {
  return (
    <AuthShell>
      <h1 className="text-lg font-semibold">Page not found</h1>
      <p className="mt-2 text-sm text-muted">
        <Link to="/projects" className="text-accent-text underline underline-offset-2 hover:no-underline">
          Back to projects
        </Link>
      </p>
    </AuthShell>
  );
}
