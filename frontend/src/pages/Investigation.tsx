import { createMemo, createResource, createSignal, For, Show } from "solid-js";
import type { Component } from "solid-js";
import { A, useParams } from "@solidjs/router";
import { api } from "../services/api";
import { formatNs } from "../utils/format";
import { Button } from "../components/Button";
import AuthControl from "../components/AuthControl";
import { canWriteInvestigations } from "../services/auth";

const hasDuration = (value?: number) => value !== undefined && Number.isFinite(value) && value > 0;
const incompleteTiming = (status?: string) =>
  !!status && /pending|queued|running|failed|insufficient|error/.test(status);

function formatRecipe(recipe: string) {
  try {
    return JSON.stringify(JSON.parse(recipe), null, 2);
  } catch {
    return recipe;
  }
}

const RunLink: Component<{ id?: number; label: string }> = (props) => (
  <Show when={props.id}>
    <A href={`/benchmarks/${props.id}`} class="text-accent hover:underline">
      {props.label} run {props.id}
    </A>
  </Show>
);

const Investigation: Component = () => {
  const params = useParams();
  const id = () => Number.parseInt(params.id ?? "", 10);
  const [evidence, { refetch }] = createResource(id, async (investigationId) => {
    if (!investigationId) return undefined;
    return api.getInvestigationEvidence(investigationId);
  });
  const [candidateCommit, setCandidateCommit] = createSignal("");
  const [candidateBranch, setCandidateBranch] = createSignal("main");
  const [candidateError, setCandidateError] = createSignal("");
  const [pairError, setPairError] = createSignal("");
  const [submitNotice, setSubmitNotice] = createSignal("");
  const [submitBusy, setSubmitBusy] = createSignal<"candidate" | "pair">();
  const candidateRequest = createMemo(() => ({
    investigationId: id(),
    body: {
      commit_hash: candidateCommit().trim(),
      branch: candidateBranch().trim() || "main",
      attempt_key: crypto.randomUUID(),
    },
  }));
  let pairRequest: { investigationId: number; body: { attempt_key: string } } | undefined;
  const refresh = () => Promise.resolve(refetch()).catch(() => undefined);
  const pairTimingReady = () => {
    const timing = evidence()?.timing;
    return (
      hasDuration(timing?.baseline_ns) &&
      hasDuration(timing?.target_ns) &&
      !incompleteTiming(timing?.comparison_status)
    );
  };
  const candidateTimingReady = () => {
    const timing = evidence()?.timing;
    return (
      hasDuration(timing?.candidate_ns) &&
      !incompleteTiming(timing?.candidate_comparison_status) &&
      (!!timing?.candidate_comparison_status || pairTimingReady())
    );
  };

  const submitCandidate = async () => {
    const request = candidateRequest();
    if (
      submitBusy() ||
      !canWriteInvestigations() ||
      !request.investigationId ||
      !request.body.commit_hash
    )
      return;
    setSubmitBusy("candidate");
    setCandidateError("");
    setSubmitNotice("");
    try {
      const result = await api.submitCandidate(request.investigationId, request.body);
      setCandidateCommit("");
      setSubmitNotice(`Candidate attempt ${result.attempt.id}: ${result.attempt.status}.`);
      await refresh();
    } catch (error) {
      setCandidateError(error instanceof Error ? error.message : String(error));
    } finally {
      setSubmitBusy(undefined);
    }
  };

  const runPairAgain = async () => {
    const investigationId = id();
    if (submitBusy() || !canWriteInvestigations() || !investigationId) return;
    if (!pairRequest || pairRequest.investigationId !== investigationId) {
      pairRequest = { investigationId, body: { attempt_key: crypto.randomUUID() } };
    }
    setSubmitBusy("pair");
    setPairError("");
    setSubmitNotice("");
    try {
      const result = await api.createInvestigationAttempt(investigationId, pairRequest.body);
      pairRequest = undefined;
      setSubmitNotice(`Pair attempt ${result.attempt.id}: ${result.attempt.status}.`);
      await refresh();
    } catch (error) {
      setPairError(error instanceof Error ? error.message : String(error));
    } finally {
      setSubmitBusy(undefined);
    }
  };

  return (
    <div class="flex h-full flex-col overflow-auto p-6">
      <div class="mb-4 flex items-center justify-between">
        <h1 class="text-[16px] font-bold tracking-wide">Investigation</h1>
        <div class="flex items-center gap-4">
          <a
            href={`/api/investigations/${id()}/evidence`}
            target="_blank"
            rel="noopener noreferrer"
            class="text-[12px] text-accent hover:underline"
          >
            Evidence JSON
          </a>
          <Button disabled={evidence.loading} onClick={() => void refresh()}>
            Refresh
          </Button>
        </div>
      </div>
      <div class="mb-4">
        <AuthControl />
      </div>
      <Show when={evidence.error}>
        <div role="alert" class="text-sm text-danger">
          {String(evidence.error)}
        </div>
      </Show>
      <Show when={submitNotice()}>
        <p role="status" class="mb-4 text-[13px]">
          {submitNotice()}
        </p>
      </Show>
      <Show when={!evidence.error && evidence()}>
        {(bundle) => (
          <div class="flex flex-col gap-6 text-[13px]">
            <section class="border border-border bg-white p-4">
              <div class="text-[11px] uppercase tracking-wider text-text-muted">Alert</div>
              <div class="mt-2 font-medium">
                {bundle().investigation.category} / {bundle().investigation.name}
              </div>
              <div class="mt-1 font-mono text-[12px] text-text-muted">
                baseline {bundle().investigation.baseline_commit.slice(0, 12)} → target{" "}
                {bundle().investigation.target_commit.slice(0, 12)}
              </div>
              <div class="mt-2 text-[12px] text-text-muted">
                Status: {bundle().investigation.status}. Scores remain uncalibrated regression
                scores. The statistical reference is not necessarily the predecessor commit.
              </div>
            </section>

            <section class="border border-border bg-white p-4">
              <div class="text-[11px] uppercase tracking-wider text-text-muted">Timing</div>
              <div class="mt-2">
                Quantity: {bundle().timing.quantity} ({bundle().timing.metric})
              </div>
              <Show when={bundle().timing.comparison_attempt_id}>
                <p class="mt-1 text-text-muted">
                  Evidence from {bundle().timing.comparison_attempt_role} attempt{" "}
                  {bundle().timing.comparison_attempt_id}.
                </p>
              </Show>
              <Show
                when={pairTimingReady()}
                fallback={
                  <p class="mt-2 text-text-muted">
                    {bundle().timing.comparison_status === "pending"
                      ? "Timing pending: waiting for the pair measurement."
                      : "Insufficient timing evidence for this pair."}
                  </p>
                }
              >
                <div class="mt-1 font-mono">
                  {formatNs(bundle().timing.baseline_ns!)} → {formatNs(bundle().timing.target_ns!)}{" "}
                  <Show when={Number.isFinite(bundle().timing.change_percent)}>
                    ({bundle().timing.change_percent?.toFixed(1)}%)
                  </Show>
                </div>
              </Show>
              <div class="text-text-muted">
                Baseline/target status: {bundle().timing.comparison_status ?? "pending"}
              </div>
              <Show when={bundle().timing.insufficient_reason}>
                <p class="mt-1 text-text-muted">{bundle().timing.insufficient_reason}</p>
              </Show>
              <div class="mt-1 flex flex-wrap gap-3 text-[12px]">
                <RunLink id={bundle().timing.baseline_run_id} label="Baseline" />
                <RunLink id={bundle().timing.target_run_id} label="Target" />
              </div>
              <Show
                when={
                  bundle().attempts.some((attempt) => attempt.role === "candidate") ||
                  bundle().timing.candidate_ns !== undefined
                }
              >
                <div class="mt-3 border-t border-border pt-2">
                  <Show
                    when={candidateTimingReady()}
                    fallback={
                      <p class="text-text-muted">
                        {bundle().timing.candidate_comparison_status === "pending"
                          ? "Candidate timing pending."
                          : "Insufficient timing evidence to compare the candidate."}
                      </p>
                    }
                  >
                    <div>
                      Candidate: {formatNs(bundle().timing.candidate_ns!)}{" "}
                      {bundle().timing.candidate_improved ? "(improved)" : ""}
                    </div>
                    <Show when={Number.isFinite(bundle().timing.candidate_change_percent)}>
                      <div>
                        {bundle().timing.candidate_change_percent?.toFixed(1)}% vs target from the
                        candidate attempt
                      </div>
                    </Show>
                    <Show when={Number.isFinite(bundle().timing.candidate_baseline_change_percent)}>
                      <div>
                        {bundle().timing.candidate_baseline_change_percent?.toFixed(1)}% vs baseline
                        from the candidate attempt
                      </div>
                    </Show>
                  </Show>
                  <Show when={bundle().timing.candidate_comparison_status}>
                    <div class="text-text-muted">
                      Candidate status: {bundle().timing.candidate_comparison_status}
                    </div>
                  </Show>
                  <div class="mt-1 flex flex-wrap gap-3 text-[12px]">
                    <RunLink id={bundle().timing.candidate_run_id} label="Candidate" />
                  </div>
                </div>
              </Show>
            </section>

            <section class="border border-border bg-white p-4">
              <div class="text-[11px] uppercase tracking-wider text-text-muted">Profiles</div>
              <div class="mt-2 text-text-muted">{bundle().profiles.meaning}</div>
              <div class="mt-1">Status: {bundle().profiles.status ?? "pending"}</div>
              <Show when={bundle().profiles.missing_reason || bundle().profiles.error}>
                <p class="mt-1 text-text-muted">
                  {bundle().profiles.error || bundle().profiles.missing_reason}
                </p>
              </Show>
              <Show when={bundle().profiles.baseline?.insufficient_reason}>
                <p class="mt-1 text-text-muted">
                  Baseline: {bundle().profiles.baseline?.insufficient_reason}
                </p>
              </Show>
              <Show when={bundle().profiles.target?.insufficient_reason}>
                <p class="mt-1 text-text-muted">
                  Target: {bundle().profiles.target?.insufficient_reason}
                </p>
              </Show>
              <Show when={bundle().profiles.functions?.length}>
                <table class="mt-3 w-full text-left text-[12px]">
                  <thead>
                    <tr class="text-[11px] uppercase text-text-muted">
                      <th class="py-1">Function</th>
                      <th class="py-1">Baseline samples</th>
                      <th class="py-1">Target samples</th>
                      <th class="py-1">Share Δ</th>
                    </tr>
                  </thead>
                  <tbody>
                    <For each={bundle().profiles.functions?.slice(0, 12)}>
                      {(fn) => (
                        <tr class="border-t border-border font-mono">
                          <td class="py-1 pr-2">{fn.name}</td>
                          <td class="py-1">{fn.baseline_samples}</td>
                          <td class="py-1">{fn.target_samples}</td>
                          <td class="py-1">{(fn.share_delta * 100).toFixed(2)}%</td>
                        </tr>
                      )}
                    </For>
                  </tbody>
                </table>
              </Show>
            </section>

            <section class="border border-border bg-white p-4">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="text-[11px] uppercase tracking-wider text-text-muted">Attempts</div>
                <Button
                  type="button"
                  disabled={!!submitBusy() || !canWriteInvestigations()}
                  onClick={() => void runPairAgain()}
                >
                  {submitBusy() === "pair"
                    ? "Submitting pair…"
                    : pairError()
                      ? "Retry pair submission"
                      : "Run pair again"}
                </Button>
              </div>
              <p class="mt-2 text-[12px] text-text-muted">
                Run pair again queues a fresh baseline and target measurement. Refresh to check
                progress.
              </p>
              <Show when={pairError()}>
                <div role="alert" class="mt-2 text-danger">
                  {pairError()}
                </div>
                <p class="mt-1 text-[12px] text-text-muted">Retry reuses the same attempt key.</p>
              </Show>
              <For each={bundle().attempts} fallback={<p class="mt-2">No attempts yet.</p>}>
                {(attempt) => (
                  <div class="mt-2 border-t border-border pt-2 font-mono text-[12px]">
                    <div class="break-all">
                      {attempt.role} · {attempt.status} · {attempt.attempt_key}
                    </div>
                    <div class="mt-1 flex flex-wrap gap-3">
                      <RunLink id={attempt.baseline_run_id} label="Baseline" />
                      <RunLink id={attempt.target_run_id} label="Target" />
                      <RunLink
                        id={attempt.run_id !== attempt.target_run_id ? attempt.run_id : undefined}
                        label={attempt.role === "candidate" ? "Candidate" : "Measured"}
                      />
                    </div>
                    <Show when={attempt.error || attempt.status === "failed"}>
                      <p class="mt-1 whitespace-pre-wrap text-danger">
                        {attempt.error || "Measurement failed."}
                      </p>
                    </Show>
                    <Show when={attempt.recipe_json}>
                      <details class="mt-2">
                        <summary class="cursor-pointer">Measurement recipe</summary>
                        <pre class="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-words bg-bg-hover p-2">
                          {formatRecipe(attempt.recipe_json!)}
                        </pre>
                      </details>
                    </Show>
                  </div>
                )}
              </For>
            </section>

            <section class="border border-border bg-white p-4">
              <div class="text-[11px] uppercase tracking-wider text-text-muted">
                Submit candidate
              </div>
              <form
                class="mt-2 flex flex-wrap items-end gap-2"
                onSubmit={(event) => {
                  event.preventDefault();
                  void submitCandidate();
                }}
              >
                <label class="flex flex-col gap-1 text-[12px]">
                  Candidate commit hash
                  <input
                    class="border border-border px-2 py-1 font-mono text-[12px]"
                    placeholder="commit hash"
                    required
                    disabled={!!submitBusy()}
                    value={candidateCommit()}
                    onInput={(event) => {
                      setCandidateCommit(event.currentTarget.value);
                      setCandidateError("");
                    }}
                  />
                </label>
                <label class="flex flex-col gap-1 text-[12px]">
                  Candidate branch
                  <input
                    class="border border-border px-2 py-1 font-mono text-[12px]"
                    placeholder="branch"
                    disabled={!!submitBusy()}
                    value={candidateBranch()}
                    onInput={(event) => {
                      setCandidateBranch(event.currentTarget.value);
                      setCandidateError("");
                    }}
                  />
                </label>
                <Button
                  type="submit"
                  variant="primary"
                  disabled={
                    !!submitBusy() || !canWriteInvestigations() || !candidateCommit().trim()
                  }
                >
                  {submitBusy() === "candidate"
                    ? "Submitting candidate…"
                    : candidateError()
                      ? "Retry candidate submission"
                      : "Queue measurement"}
                </Button>
              </form>
              <Show when={candidateError()}>
                <div role="alert" class="mt-2 text-danger">
                  {candidateError()}
                </div>
                <p class="mt-1 text-[12px] text-text-muted">
                  Retry reuses the same attempt key. Changing the commit or branch starts a new
                  submission.
                </p>
              </Show>
            </section>
          </div>
        )}
      </Show>
    </div>
  );
};

export default Investigation;
