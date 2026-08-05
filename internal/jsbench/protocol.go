package jsbench

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
)

const (
	Kind           = "js"
	Suite          = "core-default"
	Protocol       = int64(1)
	BunVersion     = "1.3.14"
	ZigVersion     = "0.15.2"
	ManifestDigest = "sha256:eadd082d755c58b7e8a865bd5873802974881967a4edab1c79d0fb1cba482aa0"
	Samples        = 3
	MaxSafeInteger = int64(1<<53 - 1)
)

func MatchesIdentity(suite string, protocol int64, bunVersion, zigVersion, manifestHash string) bool {
	return suite == Suite && protocol == Protocol && bunVersion == BunVersion &&
		zigVersion == ZigVersion && manifestHash == ManifestDigest
}

func MatchesJob(suite string, protocol int64, manifestHash string, samples int, profile string) bool {
	return suite == Suite && protocol == Protocol && manifestHash == ManifestDigest &&
		samples == Samples && profile == "none"
}

type Manifest struct {
	Hash            string      `json:"hash"`
	ProtocolVersion int64       `json:"protocol_version"`
	Measurement     Measurement `json:"measurement"`
	Cases           []Case      `json:"cases"`
}

type Measurement struct {
	TargetBatchMS      float64 `json:"target_batch_ms"`
	WarmupBatches      int64   `json:"warmup_batches"`
	MeasuredBatches    int64   `json:"measured_batches"`
	MaxRSDPPM          *int64  `json:"max_rsd_ppm"`
	MinBatchIterations *int64  `json:"min_batch_iterations"`
	MaxBatchIterations *int64  `json:"max_batch_iterations"`
	MaxCaseNS          *int64  `json:"max_case_ns"`
	MaxProcessNS       *int64  `json:"max_process_ns"`
}

type Case struct {
	Category        string          `json:"category"`
	Name            string          `json:"name"`
	WorkloadVersion int64           `json:"workload_version"`
	Parameters      json.RawMessage `json:"parameters"`
}

func DecodeManifest(data []byte) (Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Manifest{}, fmt.Errorf("must contain exactly one JSON value")
	}
	return manifest, nil
}

func manifestValue(manifest Manifest) (map[string]any, error) {
	cases := make([]any, len(manifest.Cases))
	for i, manifestCase := range manifest.Cases {
		parameters, err := DecodeParameters(manifestCase.Parameters)
		if err != nil {
			return nil, err
		}
		cases[i] = map[string]any{
			"category": manifestCase.Category, "name": manifestCase.Name,
			"workload_version": manifestCase.WorkloadVersion, "parameters": parameters,
		}
	}
	return map[string]any{
		"protocol_version": manifest.ProtocolVersion,
		"measurement": map[string]any{
			"target_batch_ms":      manifest.Measurement.TargetBatchMS,
			"warmup_batches":       manifest.Measurement.WarmupBatches,
			"measured_batches":     manifest.Measurement.MeasuredBatches,
			"max_rsd_ppm":          *manifest.Measurement.MaxRSDPPM,
			"min_batch_iterations": *manifest.Measurement.MinBatchIterations,
			"max_batch_iterations": *manifest.Measurement.MaxBatchIterations,
			"max_case_ns":          *manifest.Measurement.MaxCaseNS,
			"max_process_ns":       *manifest.Measurement.MaxProcessNS,
		},
		"cases": cases,
	}, nil
}

func canonicalJSON(value any) ([]byte, error) {
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(canonical.Bytes(), []byte{'\n'}), nil
}

func ManifestHash(manifest Manifest) (string, error) {
	value, err := manifestValue(manifest)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", digest), nil
}

func CanonicalManifestJSON(manifest Manifest) ([]byte, error) {
	value, err := manifestValue(manifest)
	if err != nil {
		return nil, err
	}
	value["hash"] = manifest.Hash
	return canonicalJSON(value)
}

func ValidateMeasurement(measurement Measurement) error {
	if !IsPositiveFinite(measurement.TargetBatchMS) ||
		measurement.WarmupBatches <= 0 || measurement.WarmupBatches > MaxSafeInteger ||
		measurement.MeasuredBatches < 2 || measurement.MeasuredBatches > MaxSafeInteger ||
		measurement.MaxRSDPPM == nil || *measurement.MaxRSDPPM < 0 || *measurement.MaxRSDPPM > MaxSafeInteger ||
		measurement.MinBatchIterations == nil || *measurement.MinBatchIterations <= 0 || *measurement.MinBatchIterations > MaxSafeInteger ||
		measurement.MaxBatchIterations == nil || *measurement.MaxBatchIterations < *measurement.MinBatchIterations || *measurement.MaxBatchIterations > MaxSafeInteger ||
		measurement.MaxCaseNS == nil || *measurement.MaxCaseNS <= 0 || *measurement.MaxCaseNS > MaxSafeInteger ||
		measurement.MaxProcessNS == nil || *measurement.MaxProcessNS <= 0 || *measurement.MaxProcessNS > MaxSafeInteger {
		return fmt.Errorf("invalid measurement settings")
	}
	return nil
}

func DecodeParameters(data json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var parameters map[string]any
	if err := decoder.Decode(&parameters); err != nil {
		return nil, fmt.Errorf("must be an object: %w", err)
	}
	if parameters == nil {
		return nil, fmt.Errorf("must be an object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("must contain exactly one object")
	}
	for key, value := range parameters {
		switch value := value.(type) {
		case string, bool:
		case json.Number:
			number, err := value.Float64()
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, fmt.Errorf("%q is not a finite number", key)
			}
			parameters[key] = number
		default:
			return nil, fmt.Errorf("%q must be a string, number, or boolean", key)
		}
	}
	return parameters, nil
}

func CalculateRSDPPM(values []float64) (int64, error) {
	if len(values) < 2 {
		return 0, fmt.Errorf("at least two measured batches are required")
	}
	var sum float64
	for _, value := range values {
		if !IsPositiveFinite(value) {
			return 0, fmt.Errorf("invalid batch timing")
		}
		sum += value
	}
	mean := sum / float64(len(values))
	var sumSquares float64
	for _, value := range values {
		delta := value - mean
		sumSquares += delta * delta
	}
	rsd := math.Sqrt(sumSquares/float64(len(values)-1)) / math.Abs(mean)
	value := rsd * 1_000_000
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(MaxSafeInteger) {
		return 0, fmt.Errorf("invalid RSD")
	}
	return int64(math.Floor(value + 0.5)), nil
}

func IsPositiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
