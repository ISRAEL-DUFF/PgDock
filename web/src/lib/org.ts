import { queryOptions, useQuery } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";
import { api, type Org } from "../api/client";

const key = "pgdock.org";
const listeners = new Set<() => void>();

function stored(): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

/** Remembers the organisation the user is working in. */
export function setCurrentOrg(id: string) {
  try {
    localStorage.setItem(key, id);
  } catch {
    // private window: the choice lasts until reload
  }
  memory = id;
  listeners.forEach((l) => l());
}

/** Forgets the remembered organisation (sign-out): the next person to sign in on this browser starts in their own. */
export function clearCurrentOrg() {
  try {
    localStorage.removeItem(key);
  } catch {
    // nothing stored to forget
  }
  memory = null;
  listeners.forEach((l) => l());
}

let memory: string | null = null;

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export const orgsQuery = queryOptions({ queryKey: ["orgs"], queryFn: api.orgs, staleTime: 30_000 });

/**
 * The organisation the user works in: the remembered one if they still
 * belong to it, else their personal organisation (V2 §13).
 */
export function useCurrentOrg(): { org: Org | undefined; orgs: Org[]; loading: boolean } {
  const chosen = useSyncExternalStore(subscribe, () => memory ?? stored(), () => null);
  const q = useQuery(orgsQuery);
  const orgs = q.data?.items ?? [];
  const member = orgs.find((o) => o.id === chosen);
  // A platform admin in a break-glass session works in an organisation they
  // are not a member of (V2 §2.4): load it directly.
  const outside = useQuery({
    queryKey: ["org", chosen, "outside"],
    queryFn: () => api.org(chosen!),
    enabled: !!chosen && q.isSuccess && !member,
    retry: false,
  });
  const org = member ?? (outside.data?.break_glass?.length ? outside.data : undefined) ?? orgs.find((o) => o.personal) ?? orgs[0];
  return { org, orgs: outside.data?.break_glass?.length && !member ? [...orgs, outside.data] : orgs, loading: q.isPending };
}

/** Whether role may manage the organisation (owner or admin). */
export function canManageOrg(org: Org | undefined): boolean {
  return org?.role === "owner" || org?.role === "admin";
}
