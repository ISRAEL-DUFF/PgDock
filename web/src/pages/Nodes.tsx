import { NodesPanel } from "../components/BackupSetup";
import { PageHeader } from "../components/ui";

export function NodesPage() {
  return (
    <>
      <PageHeader title="Nodes" subtitle="Hosts running Postgres, and the agent on each that runs backups, restores, and imports." />
      <NodesPanel />
    </>
  );
}
