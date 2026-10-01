import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api, setCsrfToken, type SessionState } from "../api/client";

export const sessionQuery = queryOptions({
  queryKey: ["session"],
  queryFn: async () => {
    const s = await api.session();
    setCsrfToken(s.csrf_token);
    return s;
  },
  staleTime: 30_000,
});

/** Refetches the session now (an invalidate would not refetch while no page observes it). */
export function refreshSession(qc: QueryClient) {
  return qc.fetchQuery({ ...sessionQuery, staleTime: 0 });
}

/** Stores a session state the server just returned (sign-in, setup). */
export function setSession(qc: QueryClient, s: SessionState) {
  setCsrfToken(s.csrf_token);
  qc.setQueryData(sessionQuery.queryKey, s);
}
