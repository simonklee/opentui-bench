import { onCleanup, onMount, Show } from "solid-js";
import type { Component } from "solid-js";
import { authError, authLoading, authSession, logout, refreshAuthSession } from "../services/auth";
import { Button } from "./Button";

const AuthControl: Component = () => {
  onMount(() => {
    const refresh = () => {
      if (document.visibilityState === "visible") void refreshAuthSession();
    };
    void refreshAuthSession();
    window.addEventListener("focus", refresh);
    document.addEventListener("visibilitychange", refresh);
    onCleanup(() => {
      window.removeEventListener("focus", refresh);
      document.removeEventListener("visibilitychange", refresh);
    });
  });

  const loginURL = () =>
    `/api/auth/github?${new URLSearchParams({
      return_to: window.location.pathname + window.location.search + window.location.hash,
    })}`;

  return (
    <div class="text-[12px]">
      <Show
        when={authSession()}
        fallback={
          <p class="text-text-muted">
            {authLoading() ? "Checking sign-in…" : "Sign-in status unavailable."}
          </p>
        }
      >
        {(session) => (
          <Show
            when={session().authenticated}
            fallback={
              <Show
                when={session().configured}
                fallback={
                  <p class="text-text-muted">
                    {session().authentication_required
                      ? "Read-only: GitHub sign-in is not configured on this server."
                      : "Local development: sign-in is not required."}
                  </p>
                }
              >
                <a href={loginURL()} rel="external" class="font-medium text-accent hover:underline">
                  Sign in with GitHub
                </a>
              </Show>
            }
          >
            <div class="flex flex-wrap items-center gap-2">
              <span role="status">Signed in as {session().login}</span>
              <Button type="button" disabled={authLoading()} onClick={() => void logout()}>
                Sign out
              </Button>
            </div>
          </Show>
        )}
      </Show>
      <Show when={authError()}>
        <p role="alert" class="mt-1 text-danger">
          {authError()}
        </p>
      </Show>
      <Show when={authError()}>
        <Button
          type="button"
          class="mt-2"
          disabled={authLoading()}
          onClick={() => void refreshAuthSession()}
        >
          {authLoading() ? "Checking…" : "Check sign-in"}
        </Button>
      </Show>
    </div>
  );
};

export default AuthControl;
