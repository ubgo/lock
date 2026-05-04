package filelock

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// marker is the deserialised contents of a marker file. Identity fields
// (pid, pidStart, host, acquired) are read by Acquire and used in
// staleness decisions; debug fields are informational only and never
// inform an acquire-or-fail outcome — see MISSION §4.5.
type marker struct {
	// Identity (consulted by Acquire)
	pid      int
	pidStart time.Time // best-effort process start time; zero if unknown
	host     string
	acquired time.Time

	// Debug (informational)
	strategy      string // pid-first / pid-only / time-only — empty when unset
	staleAfter    string // duration string e.g. "2h" or "none"
	maxConcurrent int    // 0 if unset (singleton)
	slot          int    // 0 for singleton, 0..N-1 for semaphore mode
	traceID       string // OTel trace ID; empty when no active span
}

const markerTimeFormat = time.RFC3339Nano

// writeMarker serialises m to w in the documented key=value format.
// Order is stable so operators reading two markers side-by-side see
// the same field layout in both.
func writeMarker(w io.Writer, m marker) error {
	bw := bufio.NewWriter(w)

	// Identity block
	if _, err := fmt.Fprintln(bw, "# Identity — read by Acquire"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(bw, "pid=%d\n", m.pid); err != nil {
		return err
	}
	if !m.pidStart.IsZero() {
		if _, err := fmt.Fprintf(bw, "pid_start=%s\n", m.pidStart.UTC().Format(markerTimeFormat)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(bw, "host=%s\n", m.host); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(bw, "acquired=%s\n", m.acquired.UTC().Format(markerTimeFormat)); err != nil {
		return err
	}

	// Debug block — only emit fields that were set, to keep markers
	// short for callers that don't use the related features.
	if m.strategy != "" || m.staleAfter != "" || m.maxConcurrent != 0 || m.slot != 0 || m.traceID != "" {
		if _, err := fmt.Fprintln(bw, ""); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(bw, "# Debug — informational only"); err != nil {
			return err
		}
		if m.strategy != "" {
			if _, err := fmt.Fprintf(bw, "strategy=%s\n", m.strategy); err != nil {
				return err
			}
		}
		if m.staleAfter != "" {
			if _, err := fmt.Fprintf(bw, "stale_after=%s\n", m.staleAfter); err != nil {
				return err
			}
		}
		if m.maxConcurrent != 0 {
			if _, err := fmt.Fprintf(bw, "max_concurrent=%d\n", m.maxConcurrent); err != nil {
				return err
			}
		}
		if m.slot != 0 {
			if _, err := fmt.Fprintf(bw, "slot=%d\n", m.slot); err != nil {
				return err
			}
		}
		if m.traceID != "" {
			if _, err := fmt.Fprintf(bw, "trace_id=%s\n", m.traceID); err != nil {
				return err
			}
		}
	}

	return bw.Flush()
}

// readMarker parses a marker file. Unknown keys are ignored so future
// versions can add fields without breaking older readers; missing keys
// leave the corresponding marker field zero. Returns an error only on
// I/O trouble or malformed numeric / time values.
func readMarker(path string) (marker, error) {
	f, err := os.Open(path)
	if err != nil {
		return marker{}, err
	}
	defer func() { _ = f.Close() }()

	var m marker
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "pid":
			n, err := strconv.Atoi(val)
			if err != nil {
				return marker{}, fmt.Errorf("filelock: marker pid: %w", err)
			}
			m.pid = n
		case "pid_start":
			t, err := time.Parse(markerTimeFormat, val)
			if err != nil {
				return marker{}, fmt.Errorf("filelock: marker pid_start: %w", err)
			}
			m.pidStart = t
		case "host":
			m.host = val
		case "acquired":
			t, err := time.Parse(markerTimeFormat, val)
			if err != nil {
				return marker{}, fmt.Errorf("filelock: marker acquired: %w", err)
			}
			m.acquired = t
		case "strategy":
			m.strategy = val
		case "stale_after":
			m.staleAfter = val
		case "max_concurrent":
			n, err := strconv.Atoi(val)
			if err != nil {
				return marker{}, fmt.Errorf("filelock: marker max_concurrent: %w", err)
			}
			m.maxConcurrent = n
		case "slot":
			n, err := strconv.Atoi(val)
			if err != nil {
				return marker{}, fmt.Errorf("filelock: marker slot: %w", err)
			}
			m.slot = n
		case "trace_id":
			m.traceID = val
		}
	}
	if err := scanner.Err(); err != nil {
		return marker{}, fmt.Errorf("filelock: marker scan: %w", err)
	}
	return m, nil
}
