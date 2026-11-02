// Copyright (c) 2026 the go-quake1/engine authors.
// SPDX-License-Identifier: GPL-2.0-or-later

package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ErrNoLoad is returned by [ParseLoad] when the text has no load line.
var ErrNoLoad = errors.New("bench: no load average in the text")

// ParseLoad extracts the 1-minute load average from uptime's output
// ("load averages: 6.66 5.84 4.22" on macOS, "load average: 0.5, 0.4,
// 0.3" on Linux; a decimal comma is accepted).
func ParseLoad(uptime string) (float64, error) {
	_, rest, ok := strings.Cut(uptime, "load average")
	if !ok {
		return 0, ErrNoLoad
	}
	_, rest, ok = strings.Cut(rest, ":")
	if !ok {
		return 0, ErrNoLoad
	}
	f := strings.Fields(rest)
	if len(f) == 0 {
		return 0, ErrNoLoad
	}
	s := strings.ReplaceAll(strings.TrimSuffix(f[0], ","), ",", ".")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrNoLoad, f[0])
	}
	return v, nil
}

// Target is one configuration measured in the rounds.
type Target struct {
	Name string
	Run  func(ctx context.Context, cfg Config) (Result, error)
}

// Round is one accepted measurement with the load around it.
type Round struct {
	Target     string
	Result     Result
	LoadBefore float64
	LoadAfter  float64
}

// Discard is a round thrown away because the 1-minute load was above the
// limit before or after it.
type Discard struct {
	Target string
	Load   float64
	After  bool
}

// ErrLoadTooHigh is returned by [Interleave] when the load stays above
// the limit for more attempts than allowed: the caller stops and
// reports instead of publishing numbers.
var ErrLoadTooHigh = errors.New("bench: the 1-minute load stayed above the limit")

// Interleave runs rounds of every target in order (A B A B ...). A round
// whose 1-minute load, read before or after, exceeds maxLoad is
// discarded and redone; after maxRedo discards in a row for one slot it
// gives up with [ErrLoadTooHigh]. wait is called between a discard and
// the retry.
func Interleave(ctx context.Context, targets []Target, cfg Config, rounds int, maxLoad float64,
	maxRedo int, load func() (float64, error), wait func(),
) ([]Round, []Discard, error) {
	var out []Round
	var disc []Discard
	for r := 0; r < rounds; r++ {
		for _, t := range targets {
			redo := 0
			for {
				if err := ctx.Err(); err != nil {
					return out, disc, err
				}
				before, err := load()
				if err != nil {
					return out, disc, err
				}
				if before > maxLoad {
					disc = append(disc, Discard{Target: t.Name, Load: before})
				} else {
					res, err := t.Run(ctx, cfg)
					if err != nil {
						return out, disc, fmt.Errorf("%s: %w", t.Name, err)
					}
					after, err := load()
					if err != nil {
						return out, disc, err
					}
					if after <= maxLoad {
						out = append(out, Round{Target: t.Name, Result: res, LoadBefore: before, LoadAfter: after})
						break
					}
					disc = append(disc, Discard{Target: t.Name, Load: after, After: true})
				}
				redo++
				if redo > maxRedo {
					return out, disc, ErrLoadTooHigh
				}
				wait()
			}
		}
	}
	return out, disc, nil
}

// Stat is the median and the spread (min, max) of a metric over rounds.
type Stat struct {
	Median, Min, Max float64
}

// StatOf summarises vals; the median of an even count is the mean of the
// two middle values. It returns the zero Stat for no values.
func StatOf(vals []float64) Stat {
	if len(vals) == 0 {
		return Stat{}
	}
	s := slices.Clone(vals)
	slices.Sort(s)
	n := len(s)
	med := s[n/2]
	if n%2 == 0 {
		med = (s[n/2-1] + s[n/2]) / 2
	}
	return Stat{Median: med, Min: s[0], Max: s[n-1]}
}

// Column is one reported metric.
type Column struct {
	Name string
	Get  func(Result) float64
}

// Columns are the metrics of the baseline table, in order.
var Columns = []Column{
	{"frame interval p50 (ms)", func(r Result) float64 { return r.Summary.Interval.P50 }},
	{"frame work mean (ms)", func(r Result) float64 { return r.Summary.Work.Mean }},
	{"frame work p50 (ms)", func(r Result) float64 { return r.Summary.Work.P50 }},
	{"frame work p95 (ms)", func(r Result) float64 { return r.Summary.Work.P95 }},
	{"frame work p99 (ms)", func(r Result) float64 { return r.Summary.Work.P99 }},
	{"engine / frame (ms)", func(r Result) float64 { return r.Split.EngineMS }},
	{"renderer / frame (ms)", func(r Result) float64 { return r.Split.RendererMS }},
	{"engine share (%)", func(r Result) float64 { return r.Split.EnginePercent }},
	{"allocs / frame", func(r Result) float64 { return r.AllocsPerFrame }},
	{"bytes / frame", func(r Result) float64 { return r.BytesPerFrame }},
}

// Table renders the rounds as a Markdown table, one row per metric and
// one column per target (in the order of names): "median (min-max)".
func Table(rounds []Round, names []string) string {
	var b strings.Builder
	b.WriteString("| metric |")
	sep := "|---|"
	for _, n := range names {
		fmt.Fprintf(&b, " %s |", n)
		sep += "---|"
	}
	b.WriteString("\n" + sep + "\n")
	for _, c := range Columns {
		fmt.Fprintf(&b, "| %s |", c.Name)
		for _, n := range names {
			var vals []float64
			for _, r := range rounds {
				if r.Target == n {
					vals = append(vals, c.Get(r.Result))
				}
			}
			s := StatOf(vals)
			fmt.Fprintf(&b, " %.3f (%.3f-%.3f) |", s.Median, s.Min, s.Max)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// LoadLog renders each round's load readings, one line per round.
func LoadLog(rounds []Round, discards []Discard) string {
	var b strings.Builder
	for i, r := range rounds {
		fmt.Fprintf(&b, "round %d %s: load1 before %.2f after %.2f\n", i+1, r.Target, r.LoadBefore, r.LoadAfter)
	}
	for _, d := range discards {
		side := "before"
		if d.After {
			side = "after"
		}
		fmt.Fprintf(&b, "discarded %s: load1 %.2f %s\n", d.Target, d.Load, side)
	}
	return b.String()
}

// ErrNoResult is returned by [ParseResult] when the output holds no
// result line.
var ErrNoResult = errors.New("bench: no JSON result line in the output")

// ParseResult decodes the Result a `quake-bench run --json` printed: the
// last non-empty line of out (the engine logs to stdout before it).
func ParseResult(out string) (Result, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	var res Result
	if err := json.Unmarshal([]byte(last), &res); err != nil {
		return Result{}, fmt.Errorf("%w: %q", ErrNoResult, truncate(last, 200))
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
