package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/pprof/profile"
)

func CaptureCPUProfile(ctx context.Context, r CmdRunner, benchBin string, benchName string, freq int) ([]byte, string, error) {
	tmp, err := os.MkdirTemp("", "opentui-prof-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)

	perfData := filepath.Join(tmp, "perf.data")
	pbGz := filepath.Join(tmp, "profile.pb.gz")

	cmd1 := exec.CommandContext(ctx, "perf", "record", "-F", strconv.Itoa(freq), "-g", "-o", perfData, "--",
		benchBin, "--bench", benchName, "--json")

	out1, err := r.CombinedOutput(ctx, cmd1)
	if err != nil {
		return nil, "", fmt.Errorf("perf record failed: %w\n%s", err, strings.TrimSpace(string(out1)))
	}

	if _, err := exec.LookPath("perf_to_profile"); err == nil {
		cmd2 := exec.CommandContext(ctx, "perf_to_profile", "-i", perfData, "-o", pbGz, "-f")
		if _, err := r.CombinedOutput(ctx, cmd2); err == nil {
			if data, err := os.ReadFile(pbGz); err == nil {
				if hasSymbols(data) {
					return data, "cpu.pprof", nil
				}
			}
		}
	}

	cmd3 := exec.CommandContext(ctx, "perf", "script", "-i", perfData)
	scriptOut, err := r.CombinedOutput(ctx, cmd3)
	if err != nil {
		return nil, "", fmt.Errorf("perf script failed: %w\n%s", err, strings.TrimSpace(string(scriptOut)))
	}

	prof, err := perfScriptToProfile(scriptOut)
	if err != nil {
		return nil, "", fmt.Errorf("parse perf script: %w", err)
	}

	var buf bytes.Buffer
	if err := prof.Write(&buf); err != nil {
		return nil, "", fmt.Errorf("write profile: %w", err)
	}

	return buf.Bytes(), "cpu.pprof", nil
}

func hasSymbols(data []byte) bool {
	p, err := profile.ParseData(data)
	if err != nil {
		return false
	}
	if len(p.Function) == 0 {
		return false
	}

	valid := 0
	for _, fn := range p.Function {
		if fn.Name != "" && !strings.HasPrefix(fn.Name, "0x") {
			valid++
		}
		if valid > 5 {
			return true
		}
	}
	return valid > 0
}

func perfScriptToProfile(script []byte) (*profile.Profile, error) {
	scanner := bufio.NewScanner(bytes.NewReader(script))

	p := &profile.Profile{
		SampleType: []*profile.ValueType{
			{Type: "samples", Unit: "count"},
			{Type: "cpu", Unit: "nanoseconds"},
		},
		PeriodType: &profile.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:     1,
	}

	locations := make(map[string]*profile.Location)
	functions := make(map[string]*profile.Function)

	var currentStack []*profile.Location

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(currentStack) > 0 {
				p.Sample = append(p.Sample, &profile.Sample{
					Location: currentStack,
					Value:    []int64{1, 1000},
				})
				currentStack = nil
			}
			continue
		}

		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, " ") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}

			symbolWithOffset := fields[1]
			symbol := symbolWithOffset
			if idx := strings.LastIndex(symbol, "+0x"); idx != -1 {
				symbol = symbol[:idx]
			}

			fnName := symbol
			if fnName == "" {
				continue
			}

			fn, ok := functions[fnName]
			if !ok {
				fn = &profile.Function{
					ID:         uint64(len(functions) + 1),
					Name:       fnName,
					SystemName: fnName,
				}
				functions[fnName] = fn
				p.Function = append(p.Function, fn)
			}

			locKey := fnName
			loc, ok := locations[locKey]
			if !ok {
				loc = &profile.Location{
					ID: uint64(len(locations) + 1),
					Line: []profile.Line{
						{Function: fn},
					},
				}
				locations[locKey] = loc
				p.Location = append(p.Location, loc)
			}

			currentStack = append(currentStack, loc)
		} else {
			if len(currentStack) > 0 {
				p.Sample = append(p.Sample, &profile.Sample{
					Location: currentStack,
					Value:    []int64{1, 1000},
				})
				currentStack = nil
			}
		}
	}

	if len(currentStack) > 0 {
		p.Sample = append(p.Sample, &profile.Sample{
			Location: currentStack,
			Value:    []int64{1, 1000},
		})
	}

	return p, nil
}
