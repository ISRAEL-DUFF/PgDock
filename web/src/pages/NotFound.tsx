import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { AuthShell } from "../components/Layout";
import { sessionQuery } from "../lib/session";

export function NotFound() {
  const { data: session } = useQuery(sessionQuery);
  const body = (
    <>
      <h1 className="text-lg font-semibold">Page not found</h1>
      <p className="mt-2 text-sm text-muted">
        <Link to="/projects" className="text-accent-text underline underline-offset-2 hover:no-underline">
          Back to projects
        </Link>
      </p>
    </>
  );
  // Signed in: a plain page, not the sign-in layout with its pitch panel.
  if (session?.authenticated) {
    return (
      <div className="flex min-h-screen flex-col items-center justify-center bg-bg px-4 text-center text-fg" data-testid="not-found">
        {body}
      </div>
    );
  }
  return <AuthShell>{body}</AuthShell>;
}
