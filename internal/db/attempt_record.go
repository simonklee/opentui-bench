package db

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type AttemptRun struct {
	Run       Run
	Results   []Result
	Artifacts []Artifact
}

const maxAttemptArtifactBytes = 32 << 20

func attemptRoles(a *InvestigationAttempt) ([]string, error) {
	switch a.Role {
	case AttemptRolePair:
		return []string{AttemptRoleBaseline, AttemptRoleTarget}, nil
	case AttemptRoleCandidate:
		return []string{AttemptRoleBaseline, AttemptRoleTarget, AttemptRoleCandidate}, nil
	default:
		return nil, fmt.Errorf("unsupported attempt role %q", a.Role)
	}
}

func attemptRunID(a *InvestigationAttempt, role string) (int64, error) {
	roles, err := attemptRoles(a)
	if err != nil {
		return 0, err
	}
	for _, allowed := range roles {
		if allowed != role {
			continue
		}
		switch role {
		case AttemptRoleBaseline:
			return a.BaselineRunID, nil
		case AttemptRoleTarget:
			return a.TargetRunID, nil
		default:
			return a.RunID, nil
		}
	}
	return 0, fmt.Errorf("role %q does not belong to this %s attempt", role, a.Role)
}

func attemptCommit(inv *Investigation, a *InvestigationAttempt, role string) string {
	if role == AttemptRoleBaseline {
		return inv.BaselineCommit
	}
	if role == AttemptRoleTarget {
		return inv.TargetCommit
	}
	return a.CommitHash
}

func requestedCommitMatches(requested, resolved string) bool {
	if requested == resolved {
		return true
	}
	if len(requested) < 4 || len(requested) > 40 || len(resolved) != 40 {
		return false
	}
	for _, hash := range []string{requested, resolved} {
		for _, c := range hash {
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return strings.EqualFold(requested, resolved[:len(requested)])
}

type attemptRecipe struct {
	CommitHash       string     `json:"commit_hash"`
	Harness          string     `json:"harness"`
	ZigOptimize      string     `json:"zig_optimize"`
	ZigVersion       string     `json:"zig_version"`
	ExecutableSHA256 string     `json:"executable_sha256"`
	BuildCommands    [][]string `json:"build_commands"`
	Filter           string     `json:"filter"`
	Bench            string     `json:"bench"`
	Samples          int        `json:"samples"`
	Profile          string     `json:"profile"`
	SelectorUnique   bool       `json:"selector_unique"`
}

func (r attemptRecipe) matchesRun(run *Run) bool {
	return r.CommitHash == run.CommitHashFull && r.ZigOptimize == run.ZigOptimize && r.ZigVersion == run.ZigVersion
}

func investigationOptimize(optimize string) bool {
	return optimize == "ReleaseFast" || optimize == "ReleaseSafe" || optimize == "ReleaseSmall"
}

func validateAttemptRecipe(recipeJSON string, inv *Investigation, a *InvestigationAttempt) (map[string]attemptRecipe, error) {
	var recipes map[string]attemptRecipe
	if err := json.Unmarshal([]byte(recipeJSON), &recipes); err != nil {
		return nil, fmt.Errorf("invalid attempt recipe: %w", err)
	}
	roles, err := attemptRoles(a)
	if err != nil {
		return nil, err
	}
	if len(recipes) != len(roles) {
		return nil, fmt.Errorf("recipe must contain every attempt role")
	}
	for _, role := range roles {
		r, ok := recipes[role]
		if !ok || r.Harness == "" || !investigationOptimize(r.ZigOptimize) || strings.TrimSpace(r.ZigVersion) == "" ||
			!r.SelectorUnique || !requestedCommitMatches(attemptCommit(inv, a, role), r.CommitHash) ||
			r.Filter != inv.Category || r.Bench != inv.Name || r.Samples != a.Samples || r.Profile != a.Profile {
			return nil, fmt.Errorf("recipe for %s must describe the requested workload and execution options", role)
		}
		if r.ExecutableSHA256 != "" {
			digest, err := hex.DecodeString(r.ExecutableSHA256)
			if err != nil || len(digest) != 32 {
				return nil, fmt.Errorf("recipe for %s has an invalid executable SHA256", role)
			}
		}
		for _, command := range r.BuildCommands {
			if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
				return nil, fmt.Errorf("recipe for %s has an empty build command", role)
			}
		}
	}
	return recipes, nil
}

func validateAttemptRun(tx *sql.Tx, a *InvestigationAttempt, inv *Investigation, side *AttemptRun) error {
	run := &side.Run
	linkedID, err := attemptRunID(a, run.AttemptRole)
	if err != nil {
		return err
	}
	if run.AttemptKey != a.AttemptKey || (run.AttemptID != 0 && run.AttemptID != a.ID) ||
		(run.ID != 0 && run.ID != linkedID) || (run.Purpose != "" && run.Purpose != PurposeInvestigation) {
		return fmt.Errorf("run request does not match its attempt key, role, or IDs")
	}
	if !investigationOptimize(run.ZigOptimize) || strings.TrimSpace(run.ZigVersion) == "" {
		return fmt.Errorf("%s run requires an optimized build and effective Zig version", run.AttemptRole)
	}
	normalizeRunIdentity(run)
	if run.CommitHashFull == "" || !requestedCommitMatches(attemptCommit(inv, a, run.AttemptRole), run.CommitHashFull) ||
		run.BenchmarkKind != inv.BenchmarkKind || run.BenchmarkSuite != "core-default" || run.ProtocolVersion != 1 {
		return fmt.Errorf("%s run does not match the requested commit and benchmark identity", run.AttemptRole)
	}
	if len(side.Results) != 1 || side.Results[0].Category != inv.Category || side.Results[0].Name != inv.Name {
		return fmt.Errorf("%s run must contain exactly the requested benchmark result", run.AttemptRole)
	}
	result := side.Results[0]
	if result.RunID != 0 && result.RunID != linkedID {
		return fmt.Errorf("result run_id does not match its attempt role")
	}
	var resultID, artifactID int64
	if linkedID != 0 {
		var storedCommit string
		if err := tx.QueryRow(`SELECT result.id, COALESCE(artifact.id, 0), runs.commit_hash_full
			FROM results result JOIN runs ON runs.id = result.run_id
			LEFT JOIN artifacts artifact ON artifact.result_id = result.id AND artifact.kind = 'cpu.pprof'
			WHERE result.run_id = ? AND result.category = ? AND result.name = ?`, linkedID, inv.Category, inv.Name).Scan(&resultID, &artifactID, &storedCommit); err != nil {
			return err
		}
		if run.CommitHashFull != storedCommit {
			return fmt.Errorf("%s run resolved commit does not match the stored attempt role", run.AttemptRole)
		}
	}
	if result.ID != 0 && result.ID != resultID {
		return fmt.Errorf("result id does not match its attempt role")
	}
	if result.SampleCount != int64(a.Samples) || len(result.Samples) > MaxInvestigationSamples ||
		(len(result.Samples) != 0 && len(result.Samples) != a.Samples) {
		return fmt.Errorf("investigation result sample count must match the requested samples")
	}
	if len(side.Artifacts) > 1 {
		return fmt.Errorf("each attempt role accepts at most one cpu.pprof artifact")
	}
	for _, artifact := range side.Artifacts {
		if (artifact.ResultID != 0 && artifact.ResultID != resultID) || (artifact.ID != 0 && artifact.ID != artifactID) {
			return fmt.Errorf("artifact IDs do not match its attempt role")
		}
		if artifact.Kind != "cpu.pprof" || len(artifact.DataBlob) == 0 || a.Profile != "cpu" {
			return fmt.Errorf("attempt artifacts must be nonempty cpu.pprof captures requested by the job")
		}
	}
	run.Purpose = PurposeInvestigation
	run.AttemptID = a.ID
	return nil
}

func validateAttemptRunOwnership(tx *sql.Tx, a *InvestigationAttempt, role string, runID int64) error {
	if _, err := attemptRunID(a, role); err != nil {
		return err
	}
	var valid bool
	if err := tx.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM runs WHERE id = ? AND purpose = ? AND attempt_id = ? AND idempotency_key = ?
	)`, runID, PurposeInvestigation, a.ID, attemptIdempotencyKey(a.AttemptKey, role)).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("run %d does not belong to attempt %s role %s", runID, a.AttemptKey, role)
	}
	return nil
}

func validateStoredAttempt(tx *sql.Tx, inv *Investigation, a *InvestigationAttempt) error {
	recipes, err := validateAttemptRecipe(a.RecipeJSON, inv, a)
	if a.Status != "completed" || err != nil ||
		(a.Role == AttemptRolePair && a.RunID != a.TargetRunID) {
		return fmt.Errorf("legacy partial attempt cannot be recovered atomically; create a new attempt")
	}
	roles, err := attemptRoles(a)
	if err != nil {
		return err
	}
	for _, role := range roles {
		id, _ := attemptRunID(a, role)
		if err := validateAttemptRunOwnership(tx, a, role, id); err != nil {
			return fmt.Errorf("legacy partial attempt cannot be recovered atomically; create a new attempt: %w", err)
		}
		var run Run
		var samples int64
		if err := tx.QueryRow(`SELECT runs.commit_hash_full, runs.zig_optimize, runs.zig_version, results.sample_count
			FROM runs JOIN results ON results.run_id = runs.id WHERE runs.id = ? AND results.category = ? AND results.name = ?`,
			id, inv.Category, inv.Name).Scan(&run.CommitHashFull, &run.ZigOptimize, &run.ZigVersion, &samples); err != nil {
			return err
		}
		if !recipes[role].matchesRun(&run) || samples != int64(a.Samples) {
			return fmt.Errorf("stored %s recipe and run provenance disagree; create a new attempt", role)
		}
		ids, err := resultIDsForRunTx(tx, id)
		if err != nil {
			return err
		}
		if len(ids) != 1 || ids[BenchmarkKey{Category: inv.Category, Name: inv.Name}] == 0 {
			return fmt.Errorf("legacy partial attempt has incomplete results; create a new attempt")
		}
	}
	return nil
}

func (db *DB) RecordAttempt(attemptKey, recipeJSON string, runs []AttemptRun, retention ProfileRetention) (*InvestigationAttempt, bool, error) {
	if err := ValidateAttemptKey(attemptKey); err != nil {
		return nil, false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := getAttemptByKey(tx, attemptKey)
	if err != nil {
		return nil, false, err
	}
	inv, err := getInvestigation(tx, a.InvestigationID)
	if err != nil {
		return nil, false, err
	}
	recipes, err := validateAttemptRecipe(recipeJSON, inv, a)
	if err != nil {
		return nil, false, err
	}
	roles, err := attemptRoles(a)
	if err != nil {
		return nil, false, err
	}
	if len(runs) != len(roles) {
		return nil, false, fmt.Errorf("attempt requires all %d roles in one recording", len(roles))
	}
	seen := make(map[string]bool, len(roles))
	var artifactBytes int64
	for i := range runs {
		if err := validateAttemptRun(tx, a, inv, &runs[i]); err != nil {
			return nil, false, err
		}
		role := runs[i].Run.AttemptRole
		if !recipes[role].matchesRun(&runs[i].Run) {
			return nil, false, fmt.Errorf("%s recipe and run commit, optimizer, and Zig version must agree", role)
		}
		if seen[role] {
			return nil, false, fmt.Errorf("duplicate attempt role %q", role)
		}
		seen[role] = true
		for _, artifact := range runs[i].Artifacts {
			artifactBytes += int64(len(artifact.DataBlob))
			if artifactBytes > maxAttemptArtifactBytes {
				return nil, false, fmt.Errorf("attempt artifacts exceed 32 MiB")
			}
		}
	}
	if a.Status == "completed" {
		if err := validateStoredAttempt(tx, inv, a); err != nil {
			return nil, false, err
		}
		return a, false, tx.Commit()
	}
	if a.Status != "pending" && a.Status != "running" {
		return nil, false, fmt.Errorf("attempt is %s; create a new attempt", a.Status)
	}
	var jobStatus string
	if err := tx.QueryRow(`SELECT status FROM jobs WHERE id = ? AND kind = 'investigate' AND attempt_key = ? AND investigation_id = ?`,
		a.JobID, a.AttemptKey, a.InvestigationID).Scan(&jobStatus); err != nil {
		return nil, false, err
	}
	if jobStatus != "pending" && jobStatus != "running" {
		return nil, false, fmt.Errorf("attempt job is %s; create a new attempt", jobStatus)
	}
	var existingRuns int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM runs WHERE attempt_id = ?`, a.ID).Scan(&existingRuns); err != nil {
		return nil, false, err
	}
	if existingRuns != 0 || a.RunID != 0 || a.BaselineRunID != 0 || a.TargetRunID != 0 {
		return nil, false, fmt.Errorf("legacy partial attempt cannot be recovered atomically; create a new attempt")
	}
	for _, side := range runs {
		id, ids, created, err := insertRunWithResultsTx(tx, &side.Run, side.Results, attemptIdempotencyKey(attemptKey, side.Run.AttemptRole))
		if err != nil {
			return nil, false, err
		}
		if !created {
			return nil, false, fmt.Errorf("legacy partial attempt cannot be recovered atomically; create a new attempt")
		}
		resultID := ids[BenchmarkKey{Category: inv.Category, Name: inv.Name}]
		for _, artifact := range side.Artifacts {
			artifact.ResultID = resultID
			if _, err := tx.Exec(`INSERT INTO artifacts (result_id, kind, data_blob, metadata, created_at) VALUES (?, ?, ?, ?, ?)`,
				artifact.ResultID, artifact.Kind, artifact.DataBlob, artifact.Metadata, artifact.CreatedAt); err != nil {
				return nil, false, err
			}
		}
		switch side.Run.AttemptRole {
		case AttemptRoleBaseline:
			a.BaselineRunID = id
		case AttemptRoleTarget:
			a.TargetRunID = id
		case AttemptRoleCandidate:
			a.RunID = id
		}
	}
	if a.Role == AttemptRolePair {
		a.RunID = a.TargetRunID
	}
	if _, _, err := pruneProfileDataTx(tx, retention, 0); err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`UPDATE investigation_attempts SET baseline_run_id = ?, target_run_id = ?, run_id = ?,
		status = 'completed', recipe_json = ?, error = NULL, updated_at = ? WHERE id = ?`,
		a.BaselineRunID, a.TargetRunID, a.RunID, recipeJSON, timeNow(), a.ID); err != nil {
		return nil, false, err
	}
	a, err = getAttemptByKey(tx, attemptKey)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return a, true, nil
}

func (db *DB) retryAttemptRun(run *Run, results []Result) (*Run, map[BenchmarkKey]int64, bool, error) {
	if err := ValidateAttemptKey(run.AttemptKey); err != nil {
		return nil, nil, false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := getAttemptByKey(tx, run.AttemptKey)
	if err != nil {
		return nil, nil, false, err
	}
	inv, err := getInvestigation(tx, a.InvestigationID)
	if err != nil {
		return nil, nil, false, err
	}
	if err := validateAttemptRun(tx, a, inv, &AttemptRun{Run: *run, Results: results}); err != nil {
		return nil, nil, false, err
	}
	if err := validateStoredAttempt(tx, inv, a); err != nil {
		return nil, nil, false, fmt.Errorf("investigation runs must be recorded atomically with RecordAttempt: %w", err)
	}
	existing, err := getRunByIdempotencyKeyTx(tx, attemptIdempotencyKey(run.AttemptKey, run.AttemptRole))
	if err != nil {
		return nil, nil, false, err
	}
	if !SameRunCohort(existing, run) || existing.CommitHashFull != run.CommitHashFull || existing.Branch != run.Branch {
		return nil, nil, false, fmt.Errorf("run identity does not match the stored attempt role")
	}
	existing.Purpose, existing.AttemptID = PurposeInvestigation, a.ID
	existing.AttemptKey, existing.AttemptRole = run.AttemptKey, run.AttemptRole
	ids, err := resultIDsForRunTx(tx, existing.ID)
	if err != nil {
		return nil, nil, false, err
	}
	return existing, ids, false, tx.Commit()
}
