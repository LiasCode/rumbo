package frontend

import (
	"bufio"
	"io"
	"os"
	"testing"
)

// A front end the host starts has the protocol where Attached finds it:
// file descriptors 3 and 4, or the handles Windows passes, and the terminal
// stays its own.
func TestAStartedFrontEndHasTheProtocol(t *testing.T) {
	self, err := os.Executable()

	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("RUMBO_TEST_ECHO", "1")

	started, err := Start([]string{self, "-test.run=^TestEchoFrontEnd$"})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.WriteString(started.Writer, "over the protocol\n"); err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(started.Reader).ReadString('\n')

	if err != nil || line != "back: over the protocol\n" {
		t.Fatalf("the front end answered %q, err = %v", line, err)
	}

	if code := started.Wait(); code != 0 {
		t.Fatalf("the front end ended with %d", code)
	}
}

// TestEchoFrontEnd is the front end the test above starts: it answers the
// one line it reads.
func TestEchoFrontEnd(t *testing.T) {
	if os.Getenv("RUMBO_TEST_ECHO") == "" {
		t.Skip("started by TestAStartedFrontEndHasTheProtocol")
	}

	reader, writer, err := Attached()

	if err != nil {
		t.Fatal(err)
	}

	line, err := bufio.NewReader(reader).ReadString('\n')

	if err != nil {
		t.Fatal(err)
	}

	if _, err := io.WriteString(writer, "back: "+line); err != nil {
		t.Fatal(err)
	}

	writer.Close()
	os.Exit(0)
}
