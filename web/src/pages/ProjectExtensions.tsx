import { ExtensionsCard } from "../components/ExtensionsCard";
import { Page } from "../components/ui";
import { useProject } from "./ProjectOverview";

/** Database → Extensions (spec §7.4), as Studio's list. */
export function ProjectExtensionsPage() {
  const { data: p } = useProject();
  if (!p) return null;
  return (
    <Page
      title="Database Extensions"
      description={
        <>
          Allow-listed extensions, created in the <span className="font-mono">public</span> schema.
          {p.tier === "shared" && " The dedicated tier allows a few more."}
        </>
      }
      testId="project-extensions"
    >
      <ExtensionsCard p={p} />
    </Page>
  );
}
