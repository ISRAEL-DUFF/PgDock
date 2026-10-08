import {
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useRef, useState, type FormEvent } from "react";
import { api, errorMessage, type Project } from "../api/client";
import type { components } from "../api/schema";
import {
  Alert,
  Badge,
  Button,
  CopyButton,
  Dialog,
  Field,
  Input,
  Page,
  Panel,
  Spinner,
  Stat,
  Switch,
  Table,
} from "../components/ui";
import { formatBytes, formatDate, relativeTime } from "../lib/format";
import { useProject } from "./ProjectOverview";

type S = components["schemas"];
type Bucket = S["StorageBucket"];

const MiB = 1024 * 1024;

/** Project → Storage (V4 §5): buckets and the files in them. The dashboard
 * acts as the platform; the project's policies govern its app's users. */
export function ProjectFilesPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return (
    <Page
      title="Storage"
      description="Files your app stores in buckets: public or private, with policies on who may read and write them."
      testId="project-files"
    >
      <Files p={p} />
    </Page>
  );
}

function Files({ p }: { p: Project }) {
  const svc = useQuery({
    queryKey: ["services", p.id],
    queryFn: () => api.backendServices(p.id),
  });
  const st = useQuery({
    queryKey: ["files", p.id],
    queryFn: () => api.files(p.id),
    enabled: !!svc.data?.enabled,
  });
  const [bucket, setBucket] = useState<string | null>(null);
  const [editing, setEditing] = useState<Bucket | "new" | null>(null);
  if (svc.isPending) return <Spinner />;
  if (svc.isError) return <Alert>{errorMessage(svc.error)}</Alert>;
  if (!svc.data.enabled)
    return (
      <Panel title="Backend services are off">
        <p className="text-[13px] text-muted">
          Storage is part of backend services.{" "}
          <Link
            to="/projects/$id/settings/api"
            params={{ id: p.id }}
            className="text-accent underline"
          >
            Turn them on in Project Settings → API
          </Link>
          .
        </p>
      </Panel>
    );
  if (st.isPending) return <Spinner />;
  if (st.isError) return <Alert>{errorMessage(st.error)}</Alert>;
  const o = st.data;
  const manage = p.my_role === "admin" || p.my_role === "developer";
  const open = o.buckets.find((b) => b.id === bucket) ?? null;
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat
          label="Stored"
          value={formatBytes(o.bytes)}
          hint={
            o.quota_bytes != null
              ? `of ${formatBytes(o.quota_bytes)}`
              : "no quota"
          }
          testId="files-stat-bytes"
        />
        <Stat label="Files" value={o.objects} />
        <Stat label="Buckets" value={o.buckets.length} />
        <Stat
          label="Largest upload"
          value={formatBytes(o.upload_max_bytes)}
          hint={o.measured_at ? `measured ${relativeTime(o.measured_at)}` : ""}
        />
      </div>
      {o.egress_blocked && (
        <Alert tone="warn" title="Downloads are paused">
          This month's file download allowance is used up. Downloads resume next
          month, or when the plan's allowance grows.
        </Alert>
      )}
      {o.transforms_blocked && (
        <Alert tone="warn" title="Image transforms are paused">
          This month's image transforms are used up; originals are still served.
        </Alert>
      )}
      {o.missing_objects > 0 && (
        <Alert tone="warn" title={`${o.missing_objects} files have no data`}>
          The nightly check
          {o.reconciled_at && ` (${relativeTime(o.reconciled_at)})`} found files
          whose data is missing, for example: {o.missing_sample.join(", ")}. A
          branch or a restore carries file metadata, not the files themselves.
        </Alert>
      )}
      <div className="grid gap-4 md:grid-cols-[260px_1fr]">
        <Panel
          title="Buckets"
          actions={
            manage && (
              <Button
                variant="primary"
                onClick={() => setEditing("new")}
                data-testid="new-bucket"
              >
                New bucket
              </Button>
            )
          }
          bodyClassName="p-0"
        >
          {o.buckets.length === 0 ? (
            <p className="px-4 py-3 text-[13px] text-muted">No buckets yet.</p>
          ) : (
            <ul className="flex flex-col py-1">
              {o.buckets.map((b) => (
                <li key={b.id}>
                  <button
                    type="button"
                    data-testid="bucket-row"
                    onClick={() => setBucket(b.id)}
                    className={
                      "flex w-full items-center justify-between gap-2 px-4 py-2 text-left text-[13px] hover:bg-surface-2" +
                      (b.id === bucket ? " bg-surface-2 font-medium" : "")
                    }
                  >
                    <span className="truncate">{b.id}</span>
                    <span className="flex items-center gap-2 text-xs text-muted">
                      {b.public && <Badge tone="accent">public</Badge>}
                      {formatBytes(b.bytes)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </Panel>
        {open ? (
          <Browser
            key={open.id}
            p={p}
            b={open}
            manage={manage}
            apiURL={svc.data.url ?? ""}
            onEdit={() => setEditing(open)}
          />
        ) : (
          <Panel>
            <p className="text-[13px] text-muted">
              {o.buckets.length
                ? "Pick a bucket to see its files."
                : "Create a bucket to store files: avatars, uploads, documents."}
            </p>
          </Panel>
        )}
      </div>
      {editing && (
        <BucketDialog
          p={p}
          b={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(id) => setBucket(id)}
          onDeleted={() => setBucket(null)}
        />
      )}
    </div>
  );
}

// ---- Files ------------------------------------------------------------------------

function Browser({
  p,
  b,
  manage,
  apiURL,
  onEdit,
}: {
  p: Project;
  b: Bucket;
  manage: boolean;
  apiURL: string;
  onEdit: () => void;
}) {
  const qc = useQueryClient();
  const [prefix, setPrefix] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [signed, setSigned] = useState<{ path: string; url: string } | null>(
    null,
  );
  const input = useRef<HTMLInputElement>(null);
  const list = useInfiniteQuery({
    queryKey: ["bucket-objects", p.id, b.id, prefix],
    queryFn: ({ pageParam }) =>
      api.bucketObjects(p.id, b.id, { prefix, cursor: pageParam, limit: 100 }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor,
  });
  const refresh = () =>
    Promise.all([
      qc.invalidateQueries({ queryKey: ["bucket-objects", p.id, b.id] }),
      qc.invalidateQueries({ queryKey: ["files", p.id] }),
    ]);
  const run = async (f: () => Promise<unknown>) => {
    setBusy(true);
    setErr(null);
    try {
      await f();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const upload = (fs: FileList | null) =>
    fs &&
    run(async () => {
      for (const f of Array.from(fs))
        await api.uploadObject(p.id, b.id, prefix + f.name, f);
      await refresh();
    });
  const remove = (path: string) =>
    run(async () => {
      if (!confirm(`Delete ${path}?`)) return;
      await api.deleteObjects(p.id, b.id, [path]);
      await refresh();
    });
  const sign = (path: string) =>
    run(async () => {
      const s = await api.signObject(p.id, b.id, path, 3600);
      setSigned({ path, url: s.signed_url });
    });
  const crumbs = prefix.split("/").filter(Boolean);
  const items = list.data?.pages.flatMap((pg) => pg.items) ?? [];
  return (
    <Panel
      title={
        <span className="flex flex-wrap items-center gap-1 text-[13px]">
          <button
            type="button"
            className="font-medium hover:underline"
            onClick={() => setPrefix("")}
          >
            {b.id}
          </button>
          {crumbs.map((c, i) => (
            <span key={i} className="flex items-center gap-1">
              <span className="text-muted">/</span>
              <button
                type="button"
                className="hover:underline"
                onClick={() =>
                  setPrefix(crumbs.slice(0, i + 1).join("/") + "/")
                }
              >
                {c}
              </button>
            </span>
          ))}
        </span>
      }
      description={
        <>
          {b.public
            ? "Public: anyone with the URL can download."
            : "Private: reads need a policy or a signed URL."}{" "}
          {b.file_size_limit != null &&
            `Files up to ${formatBytes(b.file_size_limit)}. `}
          {b.allowed_mime_types.length > 0 &&
            `Types: ${b.allowed_mime_types.join(", ")}.`}
        </>
      }
      actions={
        manage && (
          <div className="flex gap-2">
            <Button onClick={onEdit} data-testid="edit-bucket">
              Settings
            </Button>
            <Button
              variant="primary"
              busy={busy}
              onClick={() => input.current?.click()}
              data-testid="upload-file"
            >
              Upload
            </Button>
            <input
              ref={input}
              type="file"
              multiple
              hidden
              data-testid="upload-input"
              onChange={(e) => {
                void upload(e.target.files);
                e.target.value = "";
              }}
            />
          </div>
        )
      }
    >
      <div className="flex flex-col gap-3">
        {err && <Alert>{err}</Alert>}
        {manage && (
          <p className="text-xs text-muted">
            Uploads here go up to {formatBytes(50 * MiB)}; larger files go
            through the API's resumable uploads.
          </p>
        )}
        {list.isPending ? (
          <Spinner />
        ) : list.isError ? (
          <Alert>{errorMessage(list.error)}</Alert>
        ) : items.length === 0 ? (
          <p className="text-[13px] text-muted" data-testid="files-empty">
            {prefix ? "This folder is empty." : "No files yet."}
          </p>
        ) : (
          <Table head={["Name", "Size", "Type", "Updated", ""]}>
            {items.map((e) => (
              <tr key={e.path} data-testid="file-row">
                <td className="px-3 py-2 font-medium">
                  {e.folder ? (
                    <button
                      type="button"
                      className="hover:underline"
                      onClick={() => setPrefix(e.path + "/")}
                    >
                      {e.name}/
                    </button>
                  ) : (
                    e.name
                  )}
                </td>
                <td className="px-3 py-2 text-xs text-muted">
                  {e.object ? formatBytes(e.object.size) : ""}
                </td>
                <td className="px-3 py-2 text-xs text-muted">
                  {e.object?.mime_type}
                </td>
                <td className="px-3 py-2 text-xs text-muted">
                  {e.object ? formatDate(e.object.updated_at) : ""}
                </td>
                <td className="px-3 py-2 text-right whitespace-nowrap">
                  {e.object && (
                    <span className="inline-flex gap-1">
                      <a
                        className="inline-flex h-[26px] items-center rounded-md px-2 text-[12px] text-fg-light hover:bg-surface-2"
                        href={api.objectURL(p.id, b.id, e.path)}
                      >
                        Download
                      </a>
                      {b.public && apiURL ? (
                        <CopyButton
                          label="Copy URL"
                          value={`${apiURL}/storage/v1/object/public/${b.id}/${e.path.split("/").map(encodeURIComponent).join("/")}`}
                        />
                      ) : (
                        <Button variant="ghost" onClick={() => sign(e.path)}>
                          Signed URL
                        </Button>
                      )}
                      {manage && (
                        <Button
                          variant="ghost"
                          onClick={() => remove(e.path)}
                          data-testid="delete-file"
                        >
                          Delete
                        </Button>
                      )}
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
        {list.hasNextPage && (
          <Button
            busy={list.isFetchingNextPage}
            onClick={() => void list.fetchNextPage()}
          >
            Load more
          </Button>
        )}
      </div>
      {signed && (
        <Dialog
          open
          onOpenChange={(o) => !o && setSigned(null)}
          title="Signed URL"
          description={`Anyone with this link can download ${signed.path} for the next hour.`}
          footer={<CopyButton value={signed.url} />}
        >
          <Input readOnly value={signed.url} aria-label="Signed URL" />
        </Dialog>
      )}
    </Panel>
  );
}

// ---- Bucket settings ------------------------------------------------------------

function BucketDialog({
  p,
  b,
  onClose,
  onSaved,
  onDeleted,
}: {
  p: Project;
  b: Bucket | null;
  onClose: () => void;
  onSaved: (id: string) => void;
  onDeleted: () => void;
}) {
  const qc = useQueryClient();
  const [id, setId] = useState(b?.id ?? "");
  const [pub, setPub] = useState(b?.public ?? false);
  const [limit, setLimit] = useState(
    b?.file_size_limit != null ? String(b.file_size_limit / MiB) : "",
  );
  const [types, setTypes] = useState(b?.allowed_mime_types.join(", ") ?? "");
  const [cache, setCache] = useState(String(b?.cache_seconds ?? 3600));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const done = async () => {
    await qc.invalidateQueries({ queryKey: ["files", p.id] });
    onClose();
  };
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const size = limit.trim() ? Math.round(Number(limit) * MiB) : null;
    const mimes = types
      .split(",")
      .map((t) => t.trim())
      .filter(Boolean);
    try {
      if (b) {
        await api.updateBucket(p.id, b.id, {
          public: pub,
          file_size_limit: size,
          allowed_mime_types: mimes,
          cache_seconds: Number(cache),
        });
      } else {
        await api.createBucket(p.id, {
          id,
          public: pub,
          file_size_limit: size ?? undefined,
          allowed_mime_types: mimes,
          cache_seconds: Number(cache),
        });
      }
      onSaved(b?.id ?? id);
      await done();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    if (!b) return;
    const empty = b.objects > 0;
    if (
      !confirm(
        empty
          ? `Delete bucket ${b.id} and its ${b.objects} files? This can't be undone.`
          : `Delete bucket ${b.id}?`,
      )
    )
      return;
    setBusy(true);
    setErr(null);
    try {
      await api.deleteBucket(p.id, b.id, empty);
      onDeleted();
      await done();
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={b ? `Bucket ${b.id}` : "New bucket"}
      testId="bucket-dialog"
      footer={
        <>
          {b && (
            <Button variant="danger" onClick={() => void remove()} busy={busy}>
              Delete bucket
            </Button>
          )}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="bucket-form"
            variant="primary"
            busy={busy}
          >
            {b ? "Save" : "Create bucket"}
          </Button>
        </>
      }
    >
      <form id="bucket-form" className="flex flex-col gap-3" onSubmit={submit}>
        {err && <Alert>{err}</Alert>}
        {!b && (
          <Field
            label="Name"
            hint="Lowercase letters, digits, dots, dashes and underscores; it appears in file URLs."
          >
            {(fid) => (
              <Input
                id={fid}
                required
                pattern="[a-z0-9][a-z0-9_.\-]{0,62}"
                value={id}
                onChange={(e) => setId(e.target.value)}
              />
            )}
          </Field>
        )}
        <label className="flex items-center gap-2 text-[13px]">
          <Switch
            checked={pub}
            onCheckedChange={setPub}
            aria-label="Public bucket"
          />
          Public: anyone with a file's URL can download it (uploads still need a
          policy)
        </label>
        <Field label="File size limit (MiB)" hint="Empty for the plan's limit.">
          {(fid) => (
            <Input
              id={fid}
              type="number"
              min={0}
              step="any"
              value={limit}
              onChange={(e) => setLimit(e.target.value)}
            />
          )}
        </Field>
        <Field
          label="Allowed file types"
          hint="Comma-separated, such as image/*, application/pdf. Empty allows any."
        >
          {(fid) => (
            <Input
              id={fid}
              value={types}
              onChange={(e) => setTypes(e.target.value)}
            />
          )}
        </Field>
        <Field label="Browser cache (seconds)">
          {(fid) => (
            <Input
              id={fid}
              type="number"
              min={0}
              value={cache}
              onChange={(e) => setCache(e.target.value)}
            />
          )}
        </Field>
      </form>
    </Dialog>
  );
}
