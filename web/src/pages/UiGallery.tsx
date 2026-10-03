import { Copy, Pencil, Plus, Trash2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { LogoMark, Logo } from "../components/shell/Logo";
import {
  Alert,
  Badge,
  Button,
  Card,
  Checkbox,
  CodeBlock,
  CopyField,
  Dialog,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
  EmptyState,
  Field,
  Input,
  Segmented,
  Select,
  SidePanel,
  StatusBadge,
  Switch,
  Table,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  Toaster,
  Tooltip,
  TooltipProvider,
  toast,
} from "../components/ui";
import { nextTheme, useTheme } from "../lib/theme";

/**
 * Every component of the kit on one page, in the current theme
 * (docs/ui-redesign.md, phase 1). Only in development builds, at /_ui.
 */
export function UiGalleryPage() {
  const [theme, setTheme] = useTheme();
  const [panel, setPanel] = useState(false);
  const [dialog, setDialog] = useState(false);
  const [seg, setSeg] = useState<"data" | "definition">("data");
  const [on, setOn] = useState(true);
  return (
    <TooltipProvider>
      <div className="mx-auto flex max-w-5xl flex-col gap-10 px-6 py-10">
        <header className="flex items-center justify-between">
          <Logo />
          <Button onClick={() => setTheme(nextTheme(theme))}>Theme: {theme}</Button>
        </header>

        <Section title="Buttons">
          <Button variant="primary" icon={<Plus className="h-3.5 w-3.5" />}>
            New table
          </Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="default">Default</Button>
          <Button variant="ghost">Ghost</Button>
          <Button variant="warning">Warning</Button>
          <Button variant="danger" icon={<Trash2 className="h-3.5 w-3.5" />}>
            Delete
          </Button>
          <Button size="tiny">Tiny</Button>
          <Button size="medium" variant="primary">
            Medium
          </Button>
          <Button busy>Busy</Button>
          <Button disabled>Disabled</Button>
          <Button shortcut="⌘↵" variant="primary">
            Run
          </Button>
        </Section>

        <Section title="Badges">
          <Badge>muted</Badge>
          <Badge tone="accent">accent</Badge>
          <Badge tone="ok">ok</Badge>
          <Badge tone="warn">PRODUCTION</Badge>
          <Badge tone="danger">danger</Badge>
          <StatusBadge status="active" />
          <StatusBadge status="provisioning" />
          <StatusBadge status="failed" />
        </Section>

        <Section title="Form controls">
          <div className="grid w-full grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label="Name" hint="Lowercase letters and underscores.">
              {(id) => <Input id={id} placeholder="public.orders" />}
            </Field>
            <Field label="Type" error="Pick a type.">
              {(id) => (
                <Select id={id}>
                  <option>int8</option>
                  <option>text</option>
                  <option>timestamptz</option>
                </Select>
              )}
            </Field>
          </div>
          <label className="flex items-center gap-2 text-[13px]">
            <Checkbox defaultChecked /> Is nullable
          </label>
          <label className="flex items-center gap-2 text-[13px]">
            <Checkbox checked="indeterminate" /> Some selected
          </label>
          <label className="flex items-center gap-2 text-[13px]">
            <Switch checked={on} onCheckedChange={setOn} /> Row level security
          </label>
          <Segmented
            value={seg}
            onChange={setSeg}
            options={[
              { value: "data", label: "Data" },
              { value: "definition", label: "Definition" },
            ]}
          />
        </Section>

        <Section title="Tabs">
          <Tabs defaultValue="one" className="w-full">
            <TabsList>
              <TabsTrigger value="one">Columns</TabsTrigger>
              <TabsTrigger value="two">Indexes</TabsTrigger>
              <TabsTrigger value="three">Policies</TabsTrigger>
            </TabsList>
            <TabsContent value="one" className="pt-3 text-[13px] text-fg-light">
              The first tab.
            </TabsContent>
            <TabsContent value="two" className="pt-3 text-[13px] text-fg-light">
              The second tab.
            </TabsContent>
            <TabsContent value="three" className="pt-3 text-[13px] text-fg-light">
              The third tab.
            </TabsContent>
          </Tabs>
        </Section>

        <Section title="Menus, tooltips, overlays, toasts">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button>Open menu</Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent>
              <DropdownMenuLabel>Table</DropdownMenuLabel>
              <DropdownMenuItem icon={<Pencil className="h-3.5 w-3.5" />}>Edit table</DropdownMenuItem>
              <DropdownMenuItem icon={<Copy className="h-3.5 w-3.5" />} shortcut="⌘C">
                Copy name
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem danger icon={<Trash2 className="h-3.5 w-3.5" />}>
                Delete table
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <Tooltip content="A tooltip">
            <Button variant="ghost">Hover me</Button>
          </Tooltip>
          <Button onClick={() => setPanel(true)}>Side panel</Button>
          <Button onClick={() => setDialog(true)}>Dialog</Button>
          <Button onClick={() => toast.success("Table created")}>Toast</Button>
          <Button onClick={() => toast.error("Could not save the row")}>Error toast</Button>
        </Section>

        <Section title="Alerts">
          <div className="flex w-full flex-col gap-3">
            <Alert tone="danger" title="Something failed">
              The operation could not finish.
            </Alert>
            <Alert tone="warn">Backups have not run for two days.</Alert>
            <Alert tone="accent">This is a preview branch.</Alert>
          </div>
        </Section>

        <Section title="Cards, tables, code">
          <div className="grid w-full grid-cols-1 gap-4 md:grid-cols-2">
            <Card title="Connection">
              <CopyField label="Connection string" value="postgres://app:secret@db.example.com:6432/app" secret />
            </Card>
            <CodeBlock code={"select id, email\n  from auth.users\n limit 10;"} />
          </div>
          <Table head={["Name", "Type", "Default"]}>
            <tr>
              <td className="px-3 py-2">id</td>
              <td className="px-3 py-2">int8</td>
              <td className="px-3 py-2 text-muted">identity</td>
            </tr>
            <tr>
              <td className="px-3 py-2">created_at</td>
              <td className="px-3 py-2">timestamptz</td>
              <td className="px-3 py-2 text-muted">now()</td>
            </tr>
          </Table>
          <EmptyState title="No tables yet">Create one to get started.</EmptyState>
        </Section>

        <Section title="Logo">
          <LogoMark className="h-12 w-12" />
          <LogoMark />
          <Logo />
        </Section>
      </div>

      <SidePanel
        open={panel}
        onOpenChange={setPanel}
        title="Create a new table under public"
        footer={
          <>
            <Button onClick={() => setPanel(false)}>Cancel</Button>
            <Button variant="primary" onClick={() => setPanel(false)}>
              Save
            </Button>
          </>
        }
      >
        <div className="flex flex-col gap-4">
          <Field label="Name">{(id) => <Input id={id} />}</Field>
          <Field label="Description">{(id) => <Input id={id} placeholder="Optional" />}</Field>
        </div>
      </SidePanel>
      <Dialog
        open={dialog}
        onOpenChange={setDialog}
        title="Delete table"
        description="This cannot be undone."
        footer={
          <>
            <Button onClick={() => setDialog(false)}>Cancel</Button>
            <Button variant="danger" onClick={() => setDialog(false)}>
              Delete
            </Button>
          </>
        }
      />
      <Toaster />
    </TooltipProvider>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="text-[13px] tracking-wider text-muted uppercase">{title}</h2>
      <div className="flex flex-wrap items-center gap-3">{children}</div>
    </section>
  );
}
