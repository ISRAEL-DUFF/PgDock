import { lazy, Suspense, type ComponentProps } from "react";
import { Spinner } from "../ui";

const Inner = lazy(() => import("./CodeEditor").then((m) => ({ default: m.CodeEditor })));

/** CodeEditor, loading Monaco only when it is first shown (the table
 * editor's Definition view and JSON editor). */
export function LazyCodeEditor(props: ComponentProps<typeof Inner>) {
  return (
    <Suspense fallback={<Spinner />}>
      <Inner {...props} />
    </Suspense>
  );
}
