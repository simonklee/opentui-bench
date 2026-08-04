package web

import (
	"fmt"
	"net/http"
	"strconv"

	"opentui-bench/internal/db"
)

const (
	canonicalJSKind         = "js"
	canonicalJSSuite        = "core-default"
	canonicalJSProtocol     = int64(1)
	canonicalJSBunVersion   = "1.3.14"
	canonicalJSZigVersion   = "0.15.2"
	canonicalJSManifestHash = "sha256:0fa487783682b1227bfd4bf735fe1a969ea03f045bb8a68f87c1e41174cb3794"
)

type runIdentityResponse struct {
	BenchmarkKind   string `json:"benchmark_kind"`
	BenchmarkSuite  string `json:"benchmark_suite"`
	ProtocolVersion int64  `json:"protocol_version"`
	BunVersion      string `json:"bun_version"`
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
		ZigVersion: run.ZigVersion, ManifestHash: run.ManifestHash,
		ManifestJSON: run.ManifestJSON, MachineID: run.MachineID, ZigOptimize: run.ZigOptimize,
	}
}

func runFilterFromRequest(r *http.Request) (db.RunFilter, error) {
	q := r.URL.Query()
	kind := q.Get("benchmark_kind")
	if kind == "" {
		kind = "zig"
	}
	if kind != "zig" && kind != canonicalJSKind {
		return db.RunFilter{}, fmt.Errorf("benchmark_kind must be 'zig' or 'js'")
	}
	filter := db.RunFilter{
		BenchmarkKind: kind, BenchmarkSuite: q.Get("benchmark_suite"),
		BunVersion: q.Get("bun_version"), ZigVersion: q.Get("zig_version"),
		ManifestHash: q.Get("manifest_hash"), MachineID: q.Get("machine_id"),
		ZigOptimize: q.Get("zig_optimize"),
	}
	if raw := q.Get("protocol_version"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return db.RunFilter{}, fmt.Errorf("protocol_version must be a positive integer")
		}
		filter.ProtocolVersion = value
	}
	if kind == canonicalJSKind {
		if filter.BenchmarkSuite == "" {
			filter.BenchmarkSuite = canonicalJSSuite
		}
		if filter.ProtocolVersion == 0 {
			filter.ProtocolVersion = canonicalJSProtocol
		}
		if filter.BunVersion == "" {
			filter.BunVersion = canonicalJSBunVersion
		}
		if filter.ZigVersion == "" {
			filter.ZigVersion = canonicalJSZigVersion
		}
		if filter.ManifestHash == "" {
			filter.ManifestHash = canonicalJSManifestHash
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
		ManifestHash: q.Get("manifest_hash"), MachineID: q.Get("machine_id"), ZigOptimize: q.Get("zig_optimize"),
	}
	if filter.BenchmarkKind != "" && filter.BenchmarkKind != "zig" && filter.BenchmarkKind != canonicalJSKind {
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

func sameRunIdentity(a, b *db.Run) bool {
	return a.BenchmarkKind == b.BenchmarkKind && a.BenchmarkSuite == b.BenchmarkSuite &&
		a.ProtocolVersion == b.ProtocolVersion && a.BunVersion == b.BunVersion &&
		a.ZigVersion == b.ZigVersion && a.ManifestHash == b.ManifestHash &&
		a.MachineID == b.MachineID && (a.BenchmarkKind != "zig" || a.ZigOptimize == b.ZigOptimize)
}

func runMatchesFilter(run *db.Run, filter db.RunFilter) bool {
	return (filter.BenchmarkKind == "" || run.BenchmarkKind == filter.BenchmarkKind) &&
		(filter.BenchmarkSuite == "" || run.BenchmarkSuite == filter.BenchmarkSuite) &&
		(filter.ProtocolVersion == 0 || run.ProtocolVersion == filter.ProtocolVersion) &&
		(filter.BunVersion == "" || run.BunVersion == filter.BunVersion) &&
		(filter.ZigVersion == "" || run.ZigVersion == filter.ZigVersion) &&
		(filter.ManifestHash == "" || run.ManifestHash == filter.ManifestHash) &&
		(filter.MachineID == "" || run.MachineID == filter.MachineID) &&
		(filter.ZigOptimize == "" || run.ZigOptimize == filter.ZigOptimize)
}
