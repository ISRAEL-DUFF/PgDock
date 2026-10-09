import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, errorMessage, type Project } from "../api/client";
import type { components } from "../api/schema";
import { CostEstimate } from "./CostEstimate";
import { useOperationToast } from "./Toasts";
import { Alert, Button, Field, Input, Panel, Select } from "./ui";

type Plan = components["schemas"]["ResizePlan"];

/** Resizing a dedicated instance and growing its disk (V4.1 §5): a size
 * from the list or a custom one, with the cost and what will happen (a
 * restart, a switchover with HA, or a move when its node has no room)
 * before anything is queued. */
export function ResizeCard({ p }: { p: Project }) {
  const qc = useQueryClient();
  const toast = useOperationToast();
  const inst = p.instance;
  const profiles = useQuery({ queryKey: ["profiles"], queryFn: api.profiles });
  const [cpus, setCpus] = useState(inst?.cpus ?? 1);
  const [memory, setMemory] = useState(inst?.memory_mb ?? 1024);
  const [disk, setDisk] = useState(inst?.volume_gb ?? 20);
  const [plan, setPlan] = useState<Plan | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (!inst || p.tier !== "dedicated") return null;
  const changed =
    cpus !== (inst.cpus ?? 1) ||
    memory !== (inst.memory_mb ?? 1024) ||
    disk !== (inst.volume_gb ?? 20);
  const body = { cpus, memory_mb: memory, disk_gb: disk };
  const preview = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api.updateProjectInstance(p.id, {
        ...body,
        dry_run: true,
      });
      setPlan(r.plan ?? null);
    } catch (e) {
      setErr(errorMessage(e));
      setPlan(null);
    } finally {
      setBusy(false);
    }
  };
  const apply = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await api.updateProjectInstance(p.id, body);
      if (r.operation) toast(r.operation.id, `Resize · ${p.name}`);
      setPlan(null);
      await qc.invalidateQueries({ queryKey: ["projects"] });
    } catch (e) {
      setErr(errorMessage(e));
    } finally {
      setBusy(false);
    }
  };
  const what = !plan
    ? null
    : plan.move_to
      ? `Its node has no room, so the project moves to ${plan.move_to} into an instance of the new size (writes pause for a few seconds at the end).`
      : !plan.restart
        ? "The disk grows with no restart."
        : inst.ha_enabled
          ? "The standby is resized first, then the project switches over to it (a pause like a switchover's), then the old primary is resized."
          : "The instance restarts with the new size; the poolers hold clients for the few seconds it takes.";
  return (
    <Panel title="Size and disk" testId="resize-card">
      <div className="flex flex-col gap-3">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-4">
          <Field label="Size">
            {(id) => (
              <Select
                id={id}
                value={
                  profiles.data?.items.find(
                    (x) => x.cpus === cpus && x.memory_mb === memory,
                  )?.name ?? "custom"
                }
                onChange={(e) => {
                  const pr = profiles.data?.items.find(
                    (x) => x.name === e.target.value,
                  );
                  if (pr) {
                    setCpus(pr.cpus);
                    setMemory(pr.memory_mb);
                    setPlan(null);
                  }
                }}
              >
                {profiles.data?.items.map((x) => (
                  <option key={x.name} value={x.name}>
                    {x.name} ({x.cpus} vCPU, {x.memory_mb / 1024} GB)
                  </option>
                ))}
                <option value="custom">custom</option>
              </Select>
            )}
          </Field>
          <Field label="vCPU">
            {(id) => (
              <Input
                id={id}
                type="number"
                min={0.25}
                max={64}
                step={0.25}
                value={cpus}
                onChange={(e) => {
                  setCpus(Number(e.target.value));
                  setPlan(null);
                }}
              />
            )}
          </Field>
          <Field label="Memory (MB)">
            {(id) => (
              <Input
                id={id}
                type="number"
                min={512}
                step={256}
                value={memory}
                onChange={(e) => {
                  setMemory(Number(e.target.value));
                  setPlan(null);
                }}
              />
            )}
          </Field>
          <Field label="Disk (GB)" hint="Grows only.">
            {(id) => (
              <Input
                id={id}
                type="number"
                min={inst.volume_gb ?? 1}
                value={disk}
                onChange={(e) => {
                  setDisk(Number(e.target.value));
                  setPlan(null);
                }}
              />
            )}
          </Field>
        </div>
        {changed && (
          <CostEstimate
            org={p.org_id}
            what="The new size"
            req={{
              cpus,
              memory_mb: memory,
              disk_gb: disk,
              ha: inst.ha_enabled ?? false,
              region: p.region ?? undefined,
            }}
          />
        )}
        {what && <Alert tone="accent">{what}</Alert>}
        {err && <Alert>{err}</Alert>}
        <div className="flex gap-2">
          {!plan ? (
            <Button onClick={preview} busy={busy} disabled={!changed}>
              Check
            </Button>
          ) : (
            <Button variant="primary" onClick={apply} busy={busy}>
              Resize
            </Button>
          )}
        </div>
      </div>
    </Panel>
  );
}
