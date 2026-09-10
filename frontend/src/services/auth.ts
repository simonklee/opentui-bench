import { createSignal } from "solid-js";

interface AuthSession {
  configured: boolean;
  authentication_required: boolean;
  authenticated: boolean;
  login: string;
}

const [authSession, setAuthSession] = createSignal<AuthSession>();
const [authError, setAuthError] = createSignal("");
const [authLoading, setAuthLoading] = createSignal(false);
export { authSession, authError, authLoading };
let pendingRefresh: Promise<void> | undefined;
let pendingLogout: Promise<void> | undefined;
let generation = 0;

export const canWriteInvestigations = () => {
  const session = authSession();
  return !!session && (!session.authentication_required || session.authenticated);
};

export function refreshAuthSession(): Promise<void> {
  if (pendingLogout) return pendingLogout;
  if (pendingRefresh) return pendingRefresh;
  const requestGeneration = generation;
  setAuthLoading(true);
  pendingRefresh = (async () => {
    try {
      const response = await fetch("/api/auth/session", {
        credentials: "same-origin",
        cache: "no-store",
      });
      if (!response.ok) throw new Error("Could not check sign-in. Try again.");
      const session = (await response.json()) as AuthSession;
      if (requestGeneration !== generation) return;
      setAuthSession(session);
      setAuthError("");
    } catch (error) {
      if (requestGeneration !== generation) return;
      setAuthSession(undefined);
      setAuthError(error instanceof Error ? error.message : String(error));
    } finally {
      if (requestGeneration === generation) {
        setAuthLoading(false);
        pendingRefresh = undefined;
      }
    }
  })();
  return pendingRefresh;
}

export function logout(): Promise<void> {
  if (pendingLogout) return pendingLogout;
  generation++;
  pendingRefresh = undefined;
  setAuthLoading(true);
  pendingLogout = (async () => {
    try {
      const response = await fetch("/api/auth/logout", {
        method: "POST",
        credentials: "same-origin",
      });
      if (!response.ok) throw new Error("Could not sign out. Try again.");
      setAuthSession((session) =>
        session ? { ...session, authenticated: false, login: "" } : undefined,
      );
      setAuthError("");
    } catch (error) {
      setAuthError(error instanceof Error ? error.message : String(error));
    } finally {
      setAuthLoading(false);
      pendingLogout = undefined;
    }
  })();
  return pendingLogout;
}
