import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type EditColumn, type SchemaChange } from "../../api/client";
import { parseInput, prettyJSON, type Val } from "../../lib/tableEditor/cells";
import { Alert, Button, Checkbox, Dialog, Input, SidePanel, Spinner } from "../ui";

/** The row a foreign key points at, in a side panel. */
export function ForeignRowPanel({
  projectId,
  refSchema,
  refTable,
  refColumn,
  value,
  onClose,
  onOpenTable,
}: {
  projectId: string;
  refSchema: string;
  refTable: string;
  refColumn: string;
  value: string;
  onClose: () => void;
  onOpenTable: () => void;
}) {
  const q = useQuery({
    queryKey: ["fk-row", projectId, refSchema, refTable, refColumn, value],
    queryFn: () => api.tableRows(projectId, refSchema, refTable, undefined, { filters: [{ column: refColumn, op: "eq", value }] }),
  });
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      testId="fk-panel"
      title={
        <>
          Referencing row from <code className="font-mono">{refSchema}.{refTable}</code>
        </>
      }
      description={`where ${refColumn} = ${value}`}
      footer={
        <Button onClick={onOpenTable}>
          Open {refTable}
        </Button>
      }
    >
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <Alert>{errorMessage(q.error)}</Alert>
      ) : q.data.rows.length === 0 ? (
        <p className="text-[13px] text-muted">No such row.</p>
      ) : (
        <dl className="flex flex-col divide-y divide-line rounded-md border border-line">
          {q.data.columns.map((c, i) => (
            <div key={c.name} className="grid grid-cols-[10rem_1fr] gap-3 px-3 py-2">
              <dt className="font-mono text-[12px] text-muted">{c.name}</dt>
              <dd className="font-mono text-[12px] break-all">{q.data.rows[0][i] ?? <span className="text-muted">NULL</span>}</dd>
            </div>
          ))}
        </dl>
      )}
    </SidePanel>
  );
}

/** A JSON cell in a larger editor, or read-only. */
export function JsonPanel({ col, value, readOnly, onClose, onSave }: { col: EditColumn; value: Val; readOnly: boolean; onClose: () => void; onSave: (v: Val) => void }) {
  const [text, setText] = useState(prettyJSON(value));
  const [err, setErr] = useState<string | null>(null);
  return (
    <SidePanel
      open
      onOpenChange={(o) => !o && onClose()}
      size="large"
      testId="json-panel"
      title={
        <>
          {readOnly ? "Viewing" : "Editing"} JSON field: <code className="font-mono">{col.name}</code>
        </>
      }
      footer={
        readOnly ? (
          <Button onClick={onClose}>Close</Button>
        ) : (
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button
              variant="primary"
              onClick={() => {
                const r = parseInput(col, text.trim());
                if ("error" in r) return setErr(r.error);
                onSave(r.value);
              }}
              data-testid="json-save"
            >
              Save
            </Button>
          </>
        )
      }
    >
      <div className="flex h-full flex-col gap-2">
        <textarea
          aria-label={col.name}
          value={text}
          readOnly={readOnly}
          onChange={(e) => {
            setText(e.target.value);
            setErr(null);
          }}
          spellCheck={false}
          className="min-h-[24rem] flex-1 rounded-md border border-line-strong bg-code p-3 font-mono text-[12px] text-fg focus:border-accent focus:outline-none"
          data-testid="json-editor"
        />
        {err && <Alert>{err}</Alert>}
      </div>
    </SidePanel>
  );
}

/** A small form that ends in a schema change: new schema, new enum type,
 * duplicate a table. */
export function ChangeDialog({
  kind,
  schema,
  table,
  onClose,
  onReview,
}: {
  kind: "schema" | "enum" | "duplicate";
  schema: string;
  table?: string;
  onClose: () => void;
  onReview: (c: SchemaChange, after: { schema: string; table?: string }) => void;
}) {
  const [name, setName] = useState(kind === "duplicate" ? `${table}_duplicate` : "");
  const [values, setValues] = useState("");
  const [withData, setWithData] = useState(true);
  const submit = () => {
    const n = name.trim();
    if (!n) return;
    if (kind === "schema") onReview({ kind: "create_schema", name: n }, { schema: n });
    else if (kind === "enum") onReview({ kind: "create_enum", schema, name: n, values: values.split(",").map((v) => v.trim()).filter(Boolean) }, { schema });
    else onReview({ kind: "duplicate_table", schema, table, new_name: n, with_data: withData }, { schema, table: n });
  };
  const title = kind === "schema" ? "Create a new schema" : kind === "enum" ? `Create an enumerated type in ${schema}` : `Duplicate ${table}`;
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={title}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" onClick={submit} disabled={!name.trim() || (kind === "enum" && !values.trim())} data-testid="change-dialog-save">
            {kind === "duplicate" ? "Duplicate" : "Create"}
          </Button>
        </>
      }
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <label className="flex flex-col gap-1 text-[13px] text-fg-light">
          {kind === "duplicate" ? "Name of the copy" : "Name"}
          <Input value={name} onChange={(e) => setName(e.target.value)} className="font-mono" autoFocus data-testid="new-name" />
        </label>
        {kind === "enum" && (
          <label className="flex flex-col gap-1 text-[13px] text-fg-light">
            Values, in order, separated by commas
            <Input value={values} onChange={(e) => setValues(e.target.value)} className="font-mono" placeholder="draft, live, archived" />
          </label>
        )}
        {kind === "duplicate" && (
          <label className="flex items-center gap-2 text-[13px]">
            <Checkbox checked={withData} onCheckedChange={(v) => setWithData(v === true)} /> Copy the rows too
          </label>
        )}
      </form>
    </Dialog>
  );
}

/** Confirms deleting rows. */
export function DeleteRowsDialog({ count, table, onClose, onConfirm }: { count: number; table: string; onClose: () => void; onConfirm: () => Promise<void> }) {
  const [busy, setBusy] = useState(false);
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Delete ${count} row${count === 1 ? "" : "s"}`}
      description={`This deletes ${count === 1 ? "the selected row" : `the ${count} selected rows`} from ${table}. This can't be undone.`}
      testId="delete-rows-dialog"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="danger"
            busy={busy}
            onClick={async () => {
              setBusy(true);
              await onConfirm();
              setBusy(false);
            }}
            data-testid="confirm-delete-rows"
          >
            Delete
          </Button>
        </>
      }
    />
  );
}
