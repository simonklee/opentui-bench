package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"unicode"
)

type Investigation struct {
	ID                        int64
	IdentityKey               string
	TriggerResultID           int64
	Category                  string
	Name                      string
	BenchmarkKind             string
	BaselineCommit            string
	TargetCommit              string
	StatisticalReferenceRunID int64
	Status                    string
	CreatedAt                 string
	UpdatedAt                 string
}

type InvestigationAttempt struct {
	ID              int64
	AttemptKey      string
	InvestigationID int64
	Role            string
	CommitHash      string
	Branch          string
	Samples         int
	Profile         string
	JobID           int64
	RunID           int64
	BaselineRunID   int64
	TargetRunID     int64
	Status          string
	RecipeJSON      string
	Error           string
	CreatedAt       string
	UpdatedAt       string
}

type InvestigationCreate struct {
	TriggerResultID           int64
	Category                  string
	Name                      string
	BenchmarkKind             string
	BaselineCommit            string
	TargetCommit              string
	StatisticalReferenceRunID int64
	AttemptKey                string
	Branch                    string
	Samples                   int
	Profile                   string
	RequestedBy               string
}

func InvestigationIdentityKey(kind, category, name, baseline, target string) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\n%s\n%s\n%s\n%s", kind, category, name, baseline, target)
	return hex.EncodeToString(hash.Sum(nil))
}

func ValidateAttemptKey(key string) error {
	if key == "" {
		return fmt.Errorf("attempt_key is required")
	}
	if len(key) > AttemptKeyMaxLen {
		return fmt.Errorf("attempt_key exceeds %d characters", AttemptKeyMaxLen)
	}
	for _, r := range key {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == ':') {
			return fmt.Errorf("attempt_key must be ASCII letters, digits, '-', '_', or ':'")
		}
	}
	return nil
}

func (db *DB) GetInvestigation(id int64) (*Investigation, error) {
	return getInvestigation(db, id)
}

func getInvestigation(q profileStorageQuerier, id int64) (*Investigation, error) {
	var inv Investigation
	var triggerID, referenceID sql.NullInt64
	err := q.QueryRow(`
		SELECT id, identity_key, trigger_result_id, category, name, benchmark_kind,
		       baseline_commit, target_commit, statistical_reference_run_id, status, created_at, updated_at
		FROM investigations WHERE id = ?`, id).Scan(
		&inv.ID, &inv.IdentityKey, &triggerID, &inv.Category, &inv.Name, &inv.BenchmarkKind,
		&inv.BaselineCommit, &inv.TargetCommit, &referenceID, &inv.Status, &inv.CreatedAt, &inv.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	inv.TriggerResultID = triggerID.Int64
	inv.StatisticalReferenceRunID = referenceID.Int64
	return &inv, nil
}

func (db *DB) ListInvestigations(limit int) ([]Investigation, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.Query(`
		SELECT id, identity_key, trigger_result_id, category, name, benchmark_kind,
		       baseline_commit, target_commit, statistical_reference_run_id, status, created_at, updated_at
		FROM investigations ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var investigations []Investigation
	for rows.Next() {
		var inv Investigation
		var triggerID, referenceID sql.NullInt64
		if err := rows.Scan(
			&inv.ID, &inv.IdentityKey, &triggerID, &inv.Category, &inv.Name, &inv.BenchmarkKind,
			&inv.BaselineCommit, &inv.TargetCommit, &referenceID, &inv.Status, &inv.CreatedAt, &inv.UpdatedAt,
		); err != nil {
			return nil, err
		}
		inv.TriggerResultID = triggerID.Int64
		inv.StatisticalReferenceRunID = referenceID.Int64
		investigations = append(investigations, inv)
	}
	return investigations, rows.Err()
}

func (db *DB) GetAttemptByKey(attemptKey string) (*InvestigationAttempt, error) {
	return getAttemptByKey(db, attemptKey)
}

func getAttemptByKey(q profileStorageQuerier, attemptKey string) (*InvestigationAttempt, error) {
	return scanAttempt(q.QueryRow(`
		SELECT id, attempt_key, investigation_id, role, commit_hash, branch, samples, profile,
		       job_id, run_id, baseline_run_id, target_run_id, status, recipe_json, error, created_at, updated_at
		FROM investigation_attempts WHERE attempt_key = ?`, attemptKey))
}

func (db *DB) ListAttempts(investigationID int64) ([]InvestigationAttempt, error) {
	rows, err := db.Query(`
		SELECT id, attempt_key, investigation_id, role, commit_hash, branch, samples, profile,
		       job_id, run_id, baseline_run_id, target_run_id, status, recipe_json, error, created_at, updated_at
		FROM investigation_attempts WHERE investigation_id = ? ORDER BY id ASC`, investigationID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var attempts []InvestigationAttempt
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, *attempt)
	}
	return attempts, rows.Err()
}

func scanAttempt(s scanner) (*InvestigationAttempt, error) {
	var a InvestigationAttempt
	var jobID, runID, baselineRunID, targetRunID sql.NullInt64
	var errText sql.NullString
	if err := s.Scan(
		&a.ID, &a.AttemptKey, &a.InvestigationID, &a.Role, &a.CommitHash, &a.Branch, &a.Samples, &a.Profile,
		&jobID, &runID, &baselineRunID, &targetRunID, &a.Status, &a.RecipeJSON, &errText, &a.CreatedAt, &a.UpdatedAt,
	); err != nil {
		return nil, err
	}
	a.JobID = jobID.Int64
	a.RunID = runID.Int64
	a.BaselineRunID = baselineRunID.Int64
	a.TargetRunID = targetRunID.Int64
	a.Error = errText.String
	return &a, nil
}

// CreateInvestigationIfAbsent inserts an investigation and its first pair attempt.
// A matching identity key returns the existing investigation without creating a job.
func (db *DB) CreateInvestigationIfAbsent(create InvestigationCreate) (*Investigation, *InvestigationAttempt, *Job, bool, error) {
	if create.BenchmarkKind == "" {
		create.BenchmarkKind = "zig"
	}
	if create.BenchmarkKind != "zig" {
		return nil, nil, nil, false, fmt.Errorf("investigations currently support zig benchmarks only")
	}
	if create.Category == "" || create.Name == "" {
		return nil, nil, nil, false, fmt.Errorf("category and name are required")
	}
	if create.BaselineCommit == "" || create.TargetCommit == "" {
		return nil, nil, nil, false, fmt.Errorf("baseline_commit and target_commit are required")
	}
	if err := normalizeAttemptCreate(&create, AttemptRolePair); err != nil {
		return nil, nil, nil, false, err
	}

	identity := InvestigationIdentityKey(create.BenchmarkKind, create.Category, create.Name, create.BaselineCommit, create.TargetCommit)
	now := timeNow()

	tx, err := db.Begin()
	if err != nil {
		return nil, nil, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var existingID int64
	err = tx.QueryRow(`SELECT id FROM investigations WHERE identity_key = ?`, identity).Scan(&existingID)
	if err == nil {
		inv, err := getInvestigation(tx, existingID)
		if err != nil {
			return nil, nil, nil, false, err
		}
		if _, _, err := existingAttemptTx(tx, inv, create, AttemptRolePair); err != nil {
			return nil, nil, nil, false, err
		}
		return inv, nil, nil, false, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return nil, nil, nil, false, err
	}

	res, err := tx.Exec(`
		INSERT INTO investigations (identity_key, trigger_result_id, category, name, benchmark_kind,
			baseline_commit, target_commit, statistical_reference_run_id, status, created_at, updated_at)
		VALUES (?, NULLIF(?, 0), ?, ?, ?, ?, ?, NULLIF(?, 0), 'open', ?, ?)`,
		identity, create.TriggerResultID, create.Category, create.Name, create.BenchmarkKind,
		create.BaselineCommit, create.TargetCommit, create.StatisticalReferenceRunID, now, now)
	if err != nil {
		return nil, nil, nil, false, err
	}
	investigationID, err := res.LastInsertId()
	if err != nil {
		return nil, nil, nil, false, err
	}

	inv, err := getInvestigation(tx, investigationID)
	if err != nil {
		return nil, nil, nil, false, err
	}
	attempt, job, _, err := createAttemptTx(tx, inv, create, AttemptRolePair)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, nil, false, err
	}
	return inv, attempt, job, true, nil
}

func (db *DB) CreateCandidateAttempt(investigationID int64, create InvestigationCreate) (*InvestigationAttempt, *Job, bool, error) {
	if create.TargetCommit == "" {
		return nil, nil, false, fmt.Errorf("commit_hash is required")
	}
	return db.createAttempt(investigationID, create, AttemptRoleCandidate)
}

func (db *DB) CreatePairAttempt(investigationID int64, create InvestigationCreate) (*InvestigationAttempt, *Job, bool, error) {
	return db.createAttempt(investigationID, create, AttemptRolePair)
}

func normalizeAttemptCreate(create *InvestigationCreate, role string) error {
	if err := ValidateAttemptKey(create.AttemptKey); err != nil {
		return err
	}
	if create.Samples < 0 || create.Samples > MaxInvestigationSamples {
		return fmt.Errorf("samples must be between 1 and %d (or 0 for the default)", MaxInvestigationSamples)
	}
	if create.Samples == 0 {
		create.Samples = 3
	}
	if create.Profile == "" {
		create.Profile = "none"
		if role == AttemptRolePair {
			create.Profile = "cpu"
		}
	}
	if create.Profile != "none" && create.Profile != "cpu" {
		return fmt.Errorf("profile must be none or cpu")
	}
	if create.Branch == "" {
		create.Branch = "main"
	}
	return nil
}

func (db *DB) createAttempt(investigationID int64, create InvestigationCreate, role string) (*InvestigationAttempt, *Job, bool, error) {
	if err := normalizeAttemptCreate(&create, role); err != nil {
		return nil, nil, false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	inv, err := getInvestigation(tx, investigationID)
	if err != nil {
		return nil, nil, false, err
	}
	if (create.Category != "" && create.Category != inv.Category) ||
		(create.Name != "" && create.Name != inv.Name) ||
		(create.BenchmarkKind != "" && create.BenchmarkKind != inv.BenchmarkKind) ||
		(create.BaselineCommit != "" && create.BaselineCommit != inv.BaselineCommit) ||
		(role == AttemptRolePair && create.TargetCommit != "" && create.TargetCommit != inv.TargetCommit) {
		return nil, nil, false, fmt.Errorf("attempt selector and comparison revisions must match the investigation")
	}
	if role == AttemptRolePair {
		create.TargetCommit = inv.TargetCommit
	}
	attempt, job, created, err := createAttemptTx(tx, inv, create, role)
	if err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, err
	}
	return attempt, job, created, nil
}

func existingAttemptTx(tx *sql.Tx, inv *Investigation, create InvestigationCreate, role string) (*InvestigationAttempt, *Job, error) {
	a, err := getAttemptByKey(tx, create.AttemptKey)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if a.InvestigationID != inv.ID || a.Role != role || a.CommitHash != create.TargetCommit ||
		a.Branch != create.Branch || a.Samples != create.Samples || a.Profile != create.Profile {
		return nil, nil, fmt.Errorf("attempt_key already exists with a different investigation, role, or options")
	}
	var job Job
	if err := scanJob(tx.QueryRow(`SELECT `+jobSelectColumns+` FROM jobs WHERE id = ?`, a.JobID), &job); err != nil {
		return nil, nil, err
	}
	return a, &job, nil
}

func createAttemptTx(tx *sql.Tx, inv *Investigation, create InvestigationCreate, role string) (*InvestigationAttempt, *Job, bool, error) {
	if a, job, err := existingAttemptTx(tx, inv, create, role); a != nil || err != nil {
		return a, job, false, err
	}

	var total, candidates int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM investigation_attempts WHERE investigation_id = ?`, inv.ID).Scan(&total); err != nil {
		return nil, nil, false, err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM investigation_attempts WHERE investigation_id = ? AND role = ?`,
		inv.ID, AttemptRoleCandidate).Scan(&candidates); err != nil {
		return nil, nil, false, err
	}
	if total >= MaxAttemptsPerInvestigation {
		return nil, nil, false, fmt.Errorf("investigation has too many attempts (max %d)", MaxAttemptsPerInvestigation)
	}
	if role == AttemptRoleCandidate && candidates >= MaxCandidateAttemptsPerInvestigation {
		return nil, nil, false, fmt.Errorf("investigation has too many candidate attempts (max %d)", MaxCandidateAttemptsPerInvestigation)
	}

	var outstanding int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = ? AND status IN ('pending', 'running')`, JobKindInvestigate).Scan(&outstanding); err != nil {
		return nil, nil, false, err
	}
	if outstanding >= MaxOutstandingInvestigationJobs {
		return nil, nil, false, fmt.Errorf("too many outstanding investigation jobs (max %d)", MaxOutstandingInvestigationJobs)
	}

	now := timeNow()
	attemptRes, err := tx.Exec(`
		INSERT INTO investigation_attempts (attempt_key, investigation_id, role, commit_hash, branch, samples, profile,
			status, recipe_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', '{}', ?, ?)`,
		create.AttemptKey, inv.ID, role, create.TargetCommit, create.Branch, create.Samples, create.Profile, now, now)
	if err != nil {
		return nil, nil, false, err
	}
	attemptID, err := attemptRes.LastInsertId()
	if err != nil {
		return nil, nil, false, err
	}

	var requestedBy *string
	if create.RequestedBy != "" {
		requestedBy = &create.RequestedBy
	}
	jobRes, err := tx.Exec(`
		INSERT INTO jobs (status, kind, branch, commit_hash, repo_url, samples, profile, notes, created_at, requested_by,
			benchmark_kind, benchmark_suite, protocol_version, manifest_hash, js_runtime, runtime_version,
			category, name, attempt_key, investigation_id, baseline_commit, role)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"pending", JobKindInvestigate, create.Branch, create.TargetCommit, "origin",
		create.Samples, create.Profile, nil, now, requestedBy,
		inv.BenchmarkKind, "core-default", int64(1), "", "", "",
		inv.Category, inv.Name, create.AttemptKey, inv.ID, inv.BaselineCommit, role)
	if err != nil {
		return nil, nil, false, err
	}
	jobID, err := jobRes.LastInsertId()
	if err != nil {
		return nil, nil, false, err
	}
	if _, err := tx.Exec(`UPDATE investigation_attempts SET job_id = ? WHERE id = ?`, jobID, attemptID); err != nil {
		return nil, nil, false, err
	}
	a, job, err := existingAttemptTx(tx, inv, create, role)
	return a, job, true, err
}

func (db *DB) AttachRunToAttempt(attemptKey, role string, runID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a, err := getAttemptByKey(tx, attemptKey)
	if err != nil {
		return err
	}
	linkedID, err := attemptRunID(a, role)
	if err != nil {
		return err
	}
	if err := validateAttemptRunOwnership(tx, a, role, runID); err != nil {
		return err
	}
	if linkedID != runID {
		return fmt.Errorf("attempt run links must be recorded atomically with RecordAttempt")
	}
	return tx.Commit()
}

func (db *DB) CompleteAttempt(attemptKey string, recipeJSON string) error {
	a, err := db.GetAttemptByKey(attemptKey)
	if err != nil {
		return err
	}
	if a.Status != "completed" {
		return fmt.Errorf("attempt must be completed atomically with RecordAttempt")
	}
	return nil
}

func (db *DB) FailAttempt(attemptKey, message string) error {
	_, err := db.Exec(`
		UPDATE investigation_attempts
		SET status = 'failed', error = ?, updated_at = ?
		WHERE attempt_key = ? AND status IN ('pending', 'running')`, message, timeNow(), attemptKey)
	return err
}

func (db *DB) UpdateInvestigationStatus(id int64, status string) error {
	_, err := db.Exec(`UPDATE investigations SET status = ?, updated_at = ? WHERE id = ?`, status, timeNow(), id)
	return err
}

func CaptureMissingReason(status ProfileFinalizeStatus) string {
	if status.ResultCount == 0 {
		return "unknown"
	}
	if status.Complete {
		return ""
	}
	if status.ProfileCount == 0 {
		return "unknown"
	}
	return "not_captured"
}
