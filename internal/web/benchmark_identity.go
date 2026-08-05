package web

import (
	"fmt"
	"net/http"
	"strconv"

	"opentui-bench/internal/db"
	"opentui-bench/internal/jsbench"
)

type runIdentityResponse struct {
	BenchmarkKind   string `json:"benchmark_kind"`
	BenchmarkSuite  string `json:"benchmark_suite"`
	ProtocolVersion int64  `json:"protocol_version"`
	BunVersion      string `json:"bun_version"`
	JSRuntime       string `json:"js_runtime"`
	RuntimeVersion  string `json:"runtime_version"`
	ZigVersion      string `json:"zig_version"`
	ManifestHash    string `json:"manifest_hash"`
	ManifestJSON    string `json:"manifest_json,omitempty"`
	MachineID       string `json:"machine_id"`
	ZigOptimize     string `json:"zig_optimize"`
}

func identityResponse(run *db.Run) runIdentityResponse {
	return runIdentityResponse{
		BenchmarkKind: run.BenchmarkKind, BenchmarkSuite: run.BenchmarkSuite,
		ProtocolVersion: run.ProtocolVersion, BunVersion: run.BunVersion,
		JSRuntime: run.JSRuntime, RuntimeVersion: run.RuntimeVersion,
		ZigVersion: run.ZigVersion, ManifestHash: run.ManifestHash,
		ManifestJSON: run.ManifestJSON, MachineID: run.MachineID, ZigOptimize: run.ZigOptimize,
	}
}

func runFilterFromRequest(r *http.Request) (db.RunFilter, error) {
	filter, err := explicitRunFilterFromRequest(r)
	if err != nil {
		return db.RunFilter{}, err
	}
	if filter.BenchmarkKind == "" {
		filter.BenchmarkKind = "zig"
	}
	if filter.BenchmarkKind == jsbench.Kind {
		if filter.BenchmarkSuite == "" {
			filter.BenchmarkSuite = jsbench.Suite
		}
		if filter.ProtocolVersion == 0 {
			filter.ProtocolVersion = jsbench.Protocol
		}
		if filter.JSRuntime == "" && r.URL.Query().Get("js_runtime") != "all" {
			filter.JSRuntime = jsbench.RuntimeBun
		}
		if filter.RuntimeVersion == "" && filter.JSRuntime != "" {
			filter.RuntimeVersion = jsbench.RuntimeVersion(filter.JSRuntime)
		}
		if filter.ZigVersion == "" {
			filter.ZigVersion = jsbench.ZigVersion
		}
		if filter.ManifestHash == "" {
			filter.ManifestHash = jsbench.ManifestDigest
		}
	} else {
		if filter.BenchmarkSuite == "" {
			filter.BenchmarkSuite = "core-default"
		}
		if filter.ProtocolVersion == 0 {
			filter.ProtocolVersion = 1
		}
	}
	return filter, nil
}

func explicitRunFilterFromRequest(r *http.Request) (db.RunFilter, error) {
	q := r.URL.Query()
	filter := db.RunFilter{
		BenchmarkKind: q.Get("benchmark_kind"), BenchmarkSuite: q.Get("benchmark_suite"),
		BunVersion: q.Get("bun_version"), ZigVersion: q.Get("zig_version"),
		JSRuntime: q.Get("js_runtime"), RuntimeVersion: q.Get("runtime_version"),
		ManifestHash: q.Get("manifest_hash"), MachineID: q.Get("machine_id"), ZigOptimize: q.Get("zig_optimize"),
	}
	if filter.JSRuntime == "all" {
		filter.JSRuntime = ""
	} else if filter.JSRuntime != "" && jsbench.RuntimeVersion(filter.JSRuntime) == "" {
		return db.RunFilter{}, fmt.Errorf("js_runtime must be 'bun', 'node', or 'all'")
	}
	if filter.BunVersion != "" {
		if filter.JSRuntime != "" && filter.JSRuntime != jsbench.RuntimeBun {
			return db.RunFilter{}, fmt.Errorf("bun_version is only valid with js_runtime=bun")
		}
		filter.JSRuntime, filter.RuntimeVersion = jsbench.RuntimeBun, filter.BunVersion
	}
	if filter.BenchmarkKind != "" && filter.BenchmarkKind != "zig" && filter.BenchmarkKind != jsbench.Kind {
		return db.RunFilter{}, fmt.Errorf("benchmark_kind must be 'zig' or 'js'")
	}
	if raw := q.Get("protocol_version"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return db.RunFilter{}, fmt.Errorf("protocol_version must be a positive integer")
		}
		filter.ProtocolVersion = value
	}
	return filter, nil
}
