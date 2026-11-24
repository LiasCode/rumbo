package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/LiasCode/rumbo/internal/frontends/render"
	"github.com/LiasCode/rumbo/internal/processes"
	"github.com/LiasCode/rumbo/protocol"
)

// processes implements frontend.Handlers.Processes: the host, the programs
// it started, and what each of them uses of the machine now.
func (h *host) processes(ctx context.Context) (protocol.ProcessesResult, error) {
	started := map[int]processes.Started{}

	for _, group := range h.tools.Background() {
		started[group] = processes.Started{Role: protocol.ProcessBackground}
	}

	for name, pid := range h.extensions.Pids() {
		started[pid] = processes.Started{Role: protocol.ProcessExtension, Name: name}
	}

	h.mu.Lock()

	for name, pool := range h.pools {
		for _, pid := range pool.Pids() {
			started[pid] = processes.Started{Role: protocol.ProcessProvider, Name: name}
		}
	}

	h.mu.Unlock()

	if h.frontend != 0 {
		started[h.frontend] = processes.Started{Role: protocol.ProcessFrontend}
	}

	all, err := processes.Snapshot(ctx)

	if err != nil {
		return protocol.ProcessesResult{}, err
	}

	return protocol.ProcessesResult{Processes: processes.Of(all, os.Getpid(), started)}, nil
}

// showProcesses is the host function that shows what processes answers: a
// line for each process, under the one that started it, and what they use
// together.
func (h *host) showProcesses(ctx context.Context, _ map[string]string) (protocol.HostResult, error) {
	result, err := h.processes(ctx)

	if err != nil {
		return protocol.HostResult{}, err
	}

	var out strings.Builder

	table := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	fmt.Fprint(table, "PID\tWHAT\tPROGRAM\tMEMORY\tCPU\n")

	// depth is how far under the host each process is, by its id.
	depth := map[int]int{}

	var memory int64
	var cpu float64

	for index, process := range result.Processes {
		// The host comes first, and one that left the process that started
		// it is shown under the host.
		under, found := depth[process.Parent]

		switch {
		case index == 0:
			depth[process.PID] = 0

		case found:
			depth[process.PID] = under + 1

		default:
			depth[process.PID] = 1
		}

		what := process.Role

		if process.Name != "" {
			what += " " + process.Name
		}

		fmt.Fprintf(table, "%d\t%s%s\t%s\t%s\t%s\n",
			process.PID, strings.Repeat("  ", depth[process.PID]), what, process.Command, render.Memory(process.Memory), render.ProcessorTime(process.CPU))

		memory += process.Memory
		cpu += process.CPU
	}

	_ = table.Flush()

	// Memory two processes share is counted in each, so the sum is the most
	// they can hold together.
	fmt.Fprintf(&out, "%s, at most %s of memory, %s of processor time", render.Processes(len(result.Processes)), render.Memory(memory), render.ProcessorTime(cpu))

	return protocol.HostResult{Text: out.String()}, nil
}
