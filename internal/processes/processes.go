// Package processes says what the processes of a host use of the machine:
// the host, the programs it started, and theirs. It asks ps, which answers
// the same on Linux and on macOS, and reads nothing else of the system.
package processes

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/protocol"
)

// How long ps gets to answer.
const timeout = 5 * time.Second

// Running is a process of the machine as ps reports it: who started it, the
// process group it is in, the memory it holds, in bytes, the processor time
// it has used, in seconds, and its program's name.
type Running struct {
	PID     int
	Parent  int
	Group   int
	Memory  int64
	CPU     float64
	Command string
}

// Started is a process the host started and knows by what it is: its role,
// and for a provider or an extension, which one.
type Started struct {
	Role string
	Name string
}

// Snapshot lists the processes of the machine as they are now.
func Snapshot(ctx context.Context) ([]Running, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The command comes last, since a name may have spaces in it. Its
	// arguments are left out: they may hold what nobody should be shown.
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,pgid=,rss=,time=,comm=")

	var output bytes.Buffer

	cmd.Stdout = &output

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ps did not say what the processes use: %w", err)
	}

	// ps lists itself, which is gone by the time anyone reads its answer.
	asked := cmd.Process.Pid

	return slices.DeleteFunc(parse(output.String()), func(each Running) bool { return each.PID == asked }), nil
}

// parse reads what ps printed, one process to a line. A line it cannot
// read, such as one of a process that ended while ps looked, is left out.
func parse(output string) []Running {
	var running []Running

	for line := range strings.Lines(output) {
		fields := strings.Fields(line)

		if len(fields) < 6 {
			continue
		}

		pid, errPID := strconv.Atoi(fields[0])
		parent, errParent := strconv.Atoi(fields[1])
		group, errGroup := strconv.Atoi(fields[2])
		kibibytes, errMemory := strconv.ParseInt(fields[3], 10, 64)
		cpu, timed := seconds(fields[4])

		if errPID != nil || errParent != nil || errGroup != nil || errMemory != nil || !timed {
			continue
		}

		running = append(running, Running{
			PID:     pid,
			Parent:  parent,
			Group:   group,
			Memory:  kibibytes * 1024,
			CPU:     cpu,
			Command: filepath.Base(strings.Join(fields[5:], " ")),
		})
	}

	return running
}

// seconds reads a processor time as ps writes it: [days-]hours:minutes:seconds
// on Linux, and minutes:seconds.hundredths on macOS.
func seconds(text string) (float64, bool) {
	days := 0

	if before, rest, found := strings.Cut(text, "-"); found {
		count, err := strconv.Atoi(before)

		if err != nil {
			return 0, false
		}

		days, text = count, rest
	}

	var total float64

	for part := range strings.SplitSeq(text, ":") {
		value, err := strconv.ParseFloat(part, 64)

		if err != nil || value < 0 {
			return 0, false
		}

		total = total*60 + value
	}

	return total + float64(days)*24*60*60, true
}

// Of returns the processes of the host among all of them: the host first,
// and then those under it, each after the one that started it. started
// are the processes the host knows it started, by process id; what one of
// them started is its too, and has its role and name, so the server an
// extension runs is that extension's. Anything else the host started is a
// command. A process that left the one that started it is still counted
// while it is in the process group of one the host started.
func Of(all []Running, host int, started map[int]Started) []protocol.ProcessUsage {
	children := map[int][]Running{}

	var own *Running

	for index, each := range all {
		children[each.Parent] = append(children[each.Parent], each)

		if each.PID == host {
			own = &all[index]
		}
	}

	if own == nil {
		return nil
	}

	var listed []protocol.ProcessUsage

	seen := map[int]bool{}

	var list func(process Running, as Started)

	list = func(process Running, as Started) {
		if seen[process.PID] {
			return
		}

		seen[process.PID] = true

		if known, found := started[process.PID]; found {
			as = known
		}

		listed = append(listed, protocol.ProcessUsage{
			PID:     process.PID,
			Parent:  process.Parent,
			Role:    as.Role,
			Name:    as.Name,
			Command: process.Command,
			Memory:  process.Memory,
			CPU:     process.CPU,
		})

		under := children[process.PID]
		slices.SortFunc(under, func(a, b Running) int { return a.PID - b.PID })

		for _, child := range under {
			// What the host starts is a command until it says otherwise.
			if process.PID == host {
				list(child, Started{Role: protocol.ProcessCommand})
				continue
			}

			list(child, as)
		}
	}

	list(*own, Started{Role: protocol.ProcessHost})

	// What is left of a group the host started, once the process that
	// started it is gone.
	left := slices.Clone(all)
	slices.SortFunc(left, func(a, b Running) int { return a.PID - b.PID })

	for _, each := range left {
		if known, found := started[each.Group]; found {
			list(each, known)
		}
	}

	return listed
}
