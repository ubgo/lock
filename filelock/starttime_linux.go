//go:build linux

package filelock

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// processStartTime returns the wall-clock time the kernel reports for
// pid via /proc/<pid>/stat, field 22 (starttime, in clock ticks since
// system boot). Returns the zero Time on any error so callers can
// reason about "we know the start time" vs "we don't" without a
// separate boolean.
//
// The conversion: starttime is in clock ticks (USER_HZ, almost always
// 100 on modern kernels but exposed via sysconf for portability). We
// add ticks/USER_HZ seconds to the system boot time (read once and
// cached — boot time is constant for the kernel's life).
func processStartTime(pid int) time.Time {
	if pid <= 0 {
		return time.Time{}
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}
	}
	// /proc/PID/stat format: pid (comm) state ppid ... starttime ...
	// The comm field can contain spaces and parentheses; the kernel
	// guarantees it's the LAST occurrence of ')' that closes it. Find
	// from the right.
	closeIdx := bytes.LastIndexByte(data, ')')
	if closeIdx == -1 {
		return time.Time{}
	}
	rest := strings.TrimSpace(string(data[closeIdx+1:]))
	fields := strings.Fields(rest)
	// After "(comm)" we have state(0) ppid(1) ... starttime is field 22
	// of the original record, which is index 22 - 2 = 20 of `fields`
	// (since fields[0] is `state`).
	const startTimeFieldIdx = 19
	if len(fields) <= startTimeFieldIdx {
		return time.Time{}
	}
	ticks, err := strconv.ParseInt(fields[startTimeFieldIdx], 10, 64)
	if err != nil {
		return time.Time{}
	}

	hz := userHz()
	if hz == 0 {
		return time.Time{}
	}
	bt, err := bootTime()
	if err != nil {
		return time.Time{}
	}
	secs := ticks / hz
	frac := ticks % hz
	nanos := frac * (int64(time.Second) / hz)
	return bt.Add(time.Duration(secs)*time.Second + time.Duration(nanos))
}

// bootTime returns the wall-clock time at which the system booted.
// Cached: the kernel's boot time doesn't change for the life of the
// process. Read from /proc/stat — the "btime" line gives Unix epoch
// seconds.
var (
	bootTimeOnce sync.Once
	bootTimeVal  time.Time
	bootTimeErr  error
)

func bootTime() (time.Time, error) {
	bootTimeOnce.Do(func() {
		data, err := os.ReadFile("/proc/stat")
		if err != nil {
			bootTimeErr = err
			return
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if !strings.HasPrefix(line, "btime ") {
				continue
			}
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			secs, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				bootTimeErr = err
				return
			}
			bootTimeVal = time.Unix(secs, 0).UTC()
			return
		}
		bootTimeErr = fmt.Errorf("filelock: /proc/stat: btime line not found")
	})
	return bootTimeVal, bootTimeErr
}

// userHz returns the kernel's USER_HZ value via sysconf. Cached: it's a
// kernel constant.
var (
	userHzOnce sync.Once
	userHzVal  int64
)

func userHz() int64 {
	userHzOnce.Do(func() {
		// _SC_CLK_TCK is what sysconf would return on a real system. On
		// every Linux kernel since 2.6 it has been 100, and reading the
		// real value requires cgo or a third-party syscalls package. We
		// hard-code 100 — if future kernels deviate, M11 release notes
		// flag this as a known limitation; the worst case is start-time
		// drift that makes PID-reuse detection imperfect, not unsafe.
		const fallbackHz = 100
		userHzVal = fallbackHz
	})
	return userHzVal
}
