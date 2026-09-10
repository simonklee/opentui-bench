/// <reference types="bun" />
import { expect, spyOn, test } from "bun:test";
import {
  authError,
  authLoading,
  authSession,
  canWriteInvestigations,
  logout,
  refreshAuthSession,
} from "./auth";

const signedIn = {
  configured: true,
  authentication_required: true,
  authenticated: true,
  login: "alice",
};

test.each([
  { outcome: "success", timing: "pending" },
  { outcome: "failure", timing: "pending" },
  { outcome: "success", timing: "complete" },
  { outcome: "failure", timing: "complete" },
])("logout discards stale $outcome with a $timing newer refresh", async ({ outcome, timing }) => {
  const requests: {
    url: string;
    response: ReturnType<typeof Promise.withResolvers<Response>>;
  }[] = [];
  const controlledFetch = Object.assign(
    (input: Parameters<typeof fetch>[0]) => {
      const response = Promise.withResolvers<Response>();
      requests.push({ url: String(input), response });
      return response.promise;
    },
    { preconnect: fetch.preconnect },
  );
  const fetchMock = spyOn(globalThis, "fetch").mockImplementation(controlledFetch);

  try {
    const initial = refreshAuthSession();
    requests[0]!.response.resolve(Response.json(signedIn));
    await initial;
    expect(canWriteInvestigations()).toBe(true);

    const stale = refreshAuthSession();
    const signingOut = logout();
    expect(refreshAuthSession()).toBe(signingOut);
    expect(logout()).toBe(signingOut);
    expect(requests.map((request) => request.url)).toEqual([
      "/api/auth/session",
      "/api/auth/session",
      "/api/auth/logout",
    ]);

    requests[2]!.response.resolve(new Response(null, { status: 204 }));
    await signingOut;
    expect(authSession()?.authenticated).toBe(false);
    expect(canWriteInvestigations()).toBe(false);
    expect(authLoading()).toBe(false);

    const fresh = refreshAuthSession();
    expect(requests[3]!.url).toBe("/api/auth/session");
    if (timing === "complete") {
      requests[3]!.response.reject(new Error("Fresh refresh failed"));
      await fresh;
    }

    if (outcome === "success") requests[1]!.response.resolve(Response.json(signedIn));
    else requests[1]!.response.reject(new Error("Stale refresh failed"));
    await stale;

    expect(canWriteInvestigations()).toBe(false);
    if (timing === "pending") {
      expect(authSession()?.authenticated).toBe(false);
      expect(authError()).toBe("");
      expect(authLoading()).toBe(true);
      expect(refreshAuthSession()).toBe(fresh);
      expect(requests).toHaveLength(4);
      requests[3]!.response.resolve(Response.json({ ...signedIn, login: "bob" }));
      await fresh;
      expect(authSession()?.login).toBe("bob");
      expect(canWriteInvestigations()).toBe(true);
    } else {
      expect(authSession()).toBeUndefined();
      expect(authError()).toBe("Fresh refresh failed");
    }
    expect(authLoading()).toBe(false);
  } finally {
    fetchMock.mockRestore();
  }
});
