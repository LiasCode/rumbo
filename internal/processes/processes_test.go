package processes

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/LiasCode/rumbo/protocol"
)

// ps writes a processor time in hours, minutes, and seconds on Linux, with
// the days before them once there are any, and in minutes and seconds with
// hundredths on macOS. A name may have spaces, and on macOS it is a path.
func TestWhatPsPrintsIsRead(t *testing.T) {
	read := parse("" +
		"      1       0       1 11984 00:00:02 systemd\n" +
		"   4242    4200    4242   2048 1-02:03:04 Web Content\n" +
		"  501   400   501  10240   12:34.56 /Applications/Some App.app/Contents/MacOS/Some App\n" +
		"  777   400   777      - 00:00:00 gone\n" +
		"\n")

	want := []Running{
		{PID: 1, Parent: 0, Group: 1, Memory: 11984 * 1024, CPU: 2, Command: "systemd"},
		{PID: 4242, Parent: 4200, Group: 4242, Memory: 2048 * 1024, CPU: 24*3600 + 2*3600 + 3*60 + 4, Command: "Web Content"},
		{PID: 501, Parent: 400, Group: 501, Memory: 10240 * 1024, CPU: 12*60 + 34.56, Command: "Some App"},
	}

	if !slices.Equal(read, want) {
		t.Fatalf("read %+v, want %+v", read, want)
	}
}

// The processes of a host are itself, what it started, and what those
// started, each with the role of the one the host knows above it; what it
// did not start is not among them.
func TestTheProcessesOfAHostAreThoseUnderIt(t *testing.T) {
	all := []Running{
		{PID: 1, Parent: 0, Group: 1, Command: "init"},
		{PID: 90, Parent: 1, Group: 90, Command: "shell"},
		{PID: 100, Parent: 90, Group: 100, Memory: 10, CPU: 1, Command: "rumbo"},
		{PID: 130, Parent: 100, Group: 130, Command: "docs"},
		{PID: 110, Parent: 100, Group: 100, Command: "tui"},
		{PID: 120, Parent: 100, Group: 120, Memory: 20, CPU: 2, Command: "rumbo"},
		{PID: 131, Parent: 130, Group: 130, Command: "node"},
		{PID: 132, Parent: 131, Group: 132, Command: "helper"},
		{PID: 140, Parent: 100, Group: 140, Command: "bash"},
		{PID: 141, Parent: 140, Group: 140, Command: "sleep"},
		// A server left by a background command whose shell has ended.
		{PID: 151, Parent: 1, Group: 150, Command: "server"},
		{PID: 200, Parent: 90, Group: 200, Command: "other"},
	}

	started := map[int]Started{
		110: {Role: protocol.ProcessFrontend},
		120: {Role: protocol.ProcessProvider, Name: "anthropic"},
		130: {Role: protocol.ProcessExtension, Name: "docs"},
		150: {Role: protocol.ProcessBackground},
	}

	want := []protocol.ProcessUsage{
		{PID: 100, Parent: 90, Role: protocol.ProcessHost, Command: "rumbo", Memory: 10, CPU: 1},
		{PID: 110, Parent: 100, Role: protocol.ProcessFrontend, Command: "tui"},
		{PID: 120, Parent: 100, Role: protocol.ProcessProvider, Name: "anthropic", Command: "rumbo", Memory: 20, CPU: 2},
		{PID: 130, Parent: 100, Role: protocol.ProcessExtension, Name: "docs", Command: "docs"},
		{PID: 131, Parent: 130, Role: protocol.ProcessExtension, Name: "docs", Command: "node"},
		{PID: 132, Parent: 131, Role: protocol.ProcessExtension, Name: "docs", Command: "helper"},
		{PID: 140, Parent: 100, Role: protocol.ProcessCommand, Command: "bash"},
		{PID: 141, Parent: 140, Role: protocol.ProcessCommand, Command: "sleep"},
		{PID: 151, Parent: 1, Role: protocol.ProcessBackground, Command: "server"},
	}

	if got := Of(all, 100, started); !slices.Equal(got, want) {
		t.Fatalf("the processes of the host:\n%+v\nwant:\n%+v", got, want)
	}

	if none := Of(all, 999, started); none != nil {
		t.Fatalf("the processes of a host that is not running: %+v", none)
	}
}

// The system says what this process and one it started use.
func TestTheSystemIsAsked(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "on Windows yet") {
			t.Fatalf("on Windows, err = %v", err)
		}

		return
	}

	child := exec.Command("sleep", "30")

	if err := child.Start(); err != nil {
		t.Fatal(err)
	}

	defer func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	}()

	all, err := Snapshot(context.Background())

	if err != nil {
		t.Fatal(err)
	}

	own := Of(all, os.Getpid(), nil)

	if len(own) < 2 || own[0].PID != os.Getpid() || own[0].Role != protocol.ProcessHost || own[0].Memory <= 0 {
		t.Fatalf("this process and its own: %+v", own)
	}

	index := slices.IndexFunc(own, func(each protocol.ProcessUsage) bool { return each.PID == child.Process.Pid })

	if index < 0 || own[index].Parent != os.Getpid() || own[index].Command != "sleep" || own[index].Role != protocol.ProcessCommand {
		t.Fatalf("the process this one started is not among its own: %+v", own)
	}
}
