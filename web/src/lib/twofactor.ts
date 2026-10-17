import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";

export const twoFactorQuery = { queryKey: ["me", "totp"], queryFn: api.totpStatus, staleTime: 60_000 };

/** Whether the signed-in user has two-factor sign-in on, which decides whether
 * a step-up asks for a code. True until the answer is known, so a user who has
 * it on is never shown a form that leaves the code out. */
export function useTwoFactor(): boolean {
  const q = useQuery(twoFactorQuery);
  return q.data?.enabled ?? true;
}
