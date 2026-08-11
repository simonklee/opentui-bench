import { createResource, For, onCleanup, onMount, Show } from "solid-js";
import type { Component } from "solid-js";
import { useNavigate } from "@solidjs/router";
import { AlertTriangle, Clock3, Loader2 } from "lucide-solid";
import { api } from "../services/api";
import type { Job } from "../services/api";
import { formatDate } from "../utils/format";

const statusClass: Record<string, string> = {
  pending: "bg-amber-50 text-warning border-amber-200",
  running: "bg-blue-50 text-blue-700 border-blue-200",
  completed: "bg-green-50 text-success border-green-200",
  failed: "bg-red-50 text-danger border-red-200",
  cancelled: "bg-gray-100 text-text-muted border-gray-200",
};

const formatWhen = (value?: string) => (value ? formatDate(value) : "-");

const formatElapsed = (start?: string, end?: string) => {
  if (!start) return null;
  const ms = (end ? new Date(end).getTime() : Date.now()) - new Date(start).getTime();
  if (Number.isNaN(ms) || ms < 0) return null;
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
};

const jobKind = (job: Job) =>
  job.benchmark_kind === "js"
    ? `${job.js_runtime ?? "js"} ${job.runtime_version ?? ""}`.trim()
    : "zig";

const JobStatus: Component<{ status: string }> = (props) => (
  <span
    class={`inline-flex border px-1.5 py-0.5 text-[9px] font-bold uppercase tracking-wider ${statusClass[props.status] ?? "bg-gray-100 text-text-muted border-gray-200"}`}
  >
    {props.status}
  </span>
);

const JobIdentity: Component<{ job: Job }> = (props) => (
  <div class="min-w-0">
    <div class="flex items-center gap-2">
      <span class="font-bold text-black">#{props.job.id}</span>
      <span class="truncate font-medium text-black" title={props.job.branch}>
        {props.job.branch}
      </span>
    </div>
    <div class="mt-0.5 font-mono text-[10px] text-text-muted">
      {jobKind(props.job)}
      {props.job.commit_hash ? ` · ${props.job.commit_hash.slice(0, 7)}` : " · branch HEAD"}
    </div>
  </div>
);

const JobDetails: Component<{ job: Job }> = (props) => (
  <div class="min-w-0">
    <div class="text-[11px] text-text-muted">
      {props.job.samples} sample{props.job.samples === 1 ? "" : "s"} · {props.job.profile} profile
      <Show
        when={props.job.completed_at && formatElapsed(props.job.started_at, props.job.completed_at)}
      >
        {(elapsed) => <> · took {elapsed()}</>}
      </Show>
      <Show when={props.job.requested_by}> · requested by {props.job.requested_by}</Show>
    </div>
    <Show when={props.job.notes}>
      <div class="mt-1 truncate text-[11px]" title={props.job.notes}>
        {props.job.notes}
      </div>
    </Show>
    <Show when={props.job.error}>
      <div class="mt-1 break-words text-[11px] text-danger" title={props.job.error}>
        {props.job.error}
      </div>
    </Show>
  </div>
);

const Jobs: Component = () => {
  const navigate = useNavigate();
  const [jobs, { refetch }] = createResource(async () => {
    const [pending, running, recent] = await Promise.all([
      api.getJobs(undefined, "pending", 50),
      api.getJobs(undefined, "running", 50),
      api.getJobs(undefined, undefined, 100),
    ]);
    return {
      active: [...running, ...pending],
      history: recent.filter((job) => job.status !== "pending" && job.status !== "running"),
    };
  });

  onMount(() => {
    const timer = window.setInterval(() => void refetch(), 15_000);
    onCleanup(() => window.clearInterval(timer));
  });

  const openRun = (job: Job) => {
    if (!job.run_id) return;
    const kind = job.benchmark_kind === "js" ? "js" : "zig";
    const params = new URLSearchParams({ benchmark_kind: kind });
    if (job.js_runtime) params.set("js_runtime", job.js_runtime);
    navigate(`/benchmarks/${job.run_id}?${params}`);
  };

  return (
    <div class="flex h-full w-full flex-col">
      <header class="flex min-h-[57px] flex-none items-center justify-between border-b border-border bg-bg-dark px-4 sm:px-6">
        <div>
          <h2 class="text-[13px] font-bold uppercase tracking-widest text-black sm:text-[14px]">
            Benchmark Jobs
          </h2>
          <div class="mt-0.5 text-[10px] text-text-muted">
            Worker queue and history · all benchmark kinds
          </div>
        </div>
        <div class="flex items-center gap-1.5 text-[10px] text-text-muted">
          <Clock3 size={12} /> Updates every 15s
        </div>
      </header>

      <div class="flex-1 overflow-auto bg-bg-dark p-4 sm:p-6">
        <Show when={jobs.loading && !jobs()}>
          <div class="flex h-full items-center justify-center gap-2 text-text-muted">
            <Loader2 size={18} class="animate-spin" /> Loading jobs...
          </div>
        </Show>

        <Show when={jobs.error}>
          <div class="flex min-h-48 items-center justify-center border border-danger/30 bg-red-50 text-danger">
            <div class="text-center">
              <AlertTriangle size={20} class="mx-auto mb-2" />
              <div class="font-medium">Unable to load benchmark jobs</div>
              <button
                type="button"
                class="mt-3 border border-danger px-3 py-1.5 text-[10px] font-bold uppercase tracking-wider hover:bg-red-100"
                onClick={() => void refetch()}
              >
                Retry
              </button>
            </div>
          </div>
        </Show>

        <Show when={!jobs.error && jobs()}>
          <section>
            <div class="mb-3 flex items-baseline justify-between">
              <h3 class="text-[11px] font-bold uppercase tracking-widest text-black">Active</h3>
              <span class="font-mono text-[10px] text-text-muted">{jobs()?.active.length}</span>
            </div>
            <Show
              when={jobs()!.active.length > 0}
              fallback={
                <div class="border border-border bg-white px-4 py-8 text-center text-[12px] text-text-muted">
                  No pending or running jobs
                </div>
              }
            >
              <div class="grid gap-2 lg:grid-cols-2">
                <For each={jobs()!.active}>
                  {(job) => (
                    <article class="border border-border bg-white p-4">
                      <div class="flex items-start justify-between gap-3">
                        <JobIdentity job={job} />
                        <JobStatus status={job.status} />
                      </div>
                      <div class="mt-3 border-t border-border pt-3">
                        <JobDetails job={job} />
                        <div class="mt-2 font-mono text-[10px] text-text-muted">
                          {job.status === "running" && job.started_at
                            ? `Started ${formatWhen(job.started_at)} · running ${formatElapsed(job.started_at)}`
                            : `Queued ${formatWhen(job.created_at)}`}
                        </div>
                      </div>
                    </article>
                  )}
                </For>
              </div>
            </Show>
          </section>

          <section class="mt-8">
            <div class="mb-3 flex items-baseline justify-between">
              <h3 class="text-[11px] font-bold uppercase tracking-widest text-black">History</h3>
              <span class="font-mono text-[10px] text-text-muted">Latest 100 jobs</span>
            </div>
            <Show
              when={jobs()!.history.length > 0}
              fallback={
                <div class="border border-border bg-white px-4 py-8 text-center text-[12px] text-text-muted">
                  No completed, failed, or cancelled jobs
                </div>
              }
            >
              <div class="border border-border bg-white">
                <div class="hidden grid-cols-[minmax(180px,1.2fr)_100px_minmax(220px,1.5fr)_190px] border-b-2 border-black px-4 py-3 text-[10px] font-bold uppercase tracking-widest md:grid">
                  <span>Job</span>
                  <span>Status</span>
                  <span>Details</span>
                  <span>Finished</span>
                </div>
                <For each={jobs()!.history}>
                  {(job) => (
                    <article
                      class={`grid gap-3 border-b border-border p-4 last:border-b-0 md:grid-cols-[minmax(180px,1.2fr)_100px_minmax(220px,1.5fr)_190px] md:items-start ${job.run_id ? "cursor-pointer hover:bg-bg-hover" : ""}`}
                      onClick={() => openRun(job)}
                    >
                      <JobIdentity job={job} />
                      <div>
                        <JobStatus status={job.status} />
                      </div>
                      <JobDetails job={job} />
                      <div class="font-mono text-[10px] text-text-muted">
                        <span class="md:hidden">Finished </span>
                        {formatWhen(job.completed_at)}
                        <Show when={job.run_id}>
                          <div class="mt-1 font-bold text-black">View run #{job.run_id}</div>
                        </Show>
                      </div>
                    </article>
                  )}
                </For>
              </div>
            </Show>
          </section>
        </Show>
      </div>
    </div>
  );
};

export default Jobs;
