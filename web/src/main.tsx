import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ApiRequestError } from "./api/client";
import { refreshSession } from "./lib/session";
import { applyTheme, loadTheme } from "./lib/theme";
import { makeRouter } from "./router";
import "./styles.css";

applyTheme(loadTheme());

// A 401 anywhere means the session ended (idle timeout, sign-out elsewhere):
// re-check the session, and the router sends the user to /login.
const onError = (err: unknown) => {
  if (err instanceof ApiRequestError && err.status === 401) {
    void refreshSession(queryClient).then(() => router.invalidate());
  }
};

const queryClient = new QueryClient({
  queryCache: new QueryCache({ onError }),
  mutationCache: new MutationCache({ onError }),
  defaultOptions: {
    queries: {
      retry: (n, err) => !(err instanceof ApiRequestError && err.status < 500) && n < 2,
      refetchOnWindowFocus: true,
    },
  },
});
const router = makeRouter(queryClient);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
