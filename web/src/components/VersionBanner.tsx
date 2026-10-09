import { Link } from "@tanstack/react-router";
import type { Project } from "../api/client";
import { formatDate } from "../lib/format";
import { cx } from "./ui";

/** A deprecated or retired Postgres major's banner (V4.1 §6.1). */
export function VersionBanner({ p }: { p: Project }) {
  const st = p.instance?.pg_version_status;
  if (st !== "deprecated" && st !== "retired") return null;
  const v = p.instance?.pg_version;
  const retired = st === "retired";
  return (
    <div
      className={cx(
        "mb-4 flex flex-wrap items-center justify-between gap-2 rounded-md border px-3 py-2 text-sm",
        retired
          ? "border-danger/40 bg-danger/10 text-danger-text"
          : "border-warn/40 bg-warn/10 text-warn-text",
      )}
      role="alert"
      data-testid="version-banner"
    >
      <span>
        {retired ? (
          <>
            <strong>Postgres {v} is retired.</strong> This project keeps running
            but is unsupported: upgrade it to a supported major.
          </>
        ) : (
          <>
            <strong>Postgres {v} is deprecated</strong> and retires on{" "}
            {formatDate(p.instance?.pg_version_retires_at)}. Upgrade before
            then; a preflight checks the schema on the new major first.
          </>
        )}
      </span>
      {(p.my_role === "admin" || p.my_role === undefined) && (
        <Link
          to="/projects/$id/settings/compute"
          params={{ id: p.id }}
          className="font-medium underline"
        >
          Upgrade
        </Link>
      )}
    </div>
  );
}
