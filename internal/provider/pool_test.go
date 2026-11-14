package provider

import (
	"context"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rumbo/protocol"
)

// testPool is a pool of echo providers that counts the processes it starts.
func testPool(t *testing.T) (*Pool, *atomic.Int32) {
	t.Helper()
	t.Setenv("RUMBO_TEST_PROVIDER", "1")

	var started atomic.Int32

	pool := &Pool{start: func(ctx context.Context) (pooled, error) {
		started.Add(1)
		return Start(ctx, []string{os.Args[0]}, nil, io.Discard)
	}}

	t.Cleanup(func() { pool.Close() })

	return pool, &started
}

func ask(pool *Pool, ctx context.Context, model string) (string, error) {
	result, err := pool.Complete(ctx, protocol.CompleteParams{Model: model}, nil)

	if err != nil {
		return "", err
	}

	return result.Message.Content[0].Text, nil
}

// A request that finds every process busy starts another, and processes
// are reused once their requests end.
func TestPoolStartsAProcessPerRequestRunningAtOnce(t *testing.T) {
	pool, started := testPool(t)

	if err := pool.Ready(context.Background()); err != nil || started.Load() != 1 {
		t.Fatalf("ready: err = %v, started %d", err, started.Load())
	}

	hung, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)

	go func() {
		_, err := ask(pool, hung, "hang")
		waiting <- err
	}()

	// The first process is busy now, so this one starts a second.
	for started.Load() < 2 {
		if answer, err := ask(pool, context.Background(), "first"); err != nil || answer != "first" {
			t.Fatalf("answer = %q, err = %v", answer, err)
		}

		time.Sleep(time.Millisecond)
	}

	cancel()

	if err := <-waiting; err == nil {
		t.Fatal("the cancelled request did not fail")
	}

	for range 3 {
		if _, err := ask(pool, context.Background(), "again"); err != nil {
			t.Fatal(err)
		}
	}

	if started.Load() != 2 {
		t.Fatalf("started %d processes for at most two requests at once", started.Load())
	}
}

// A process that died is not handed out again: the next request starts a
// new one and succeeds.
func TestPoolReplacesAProcessThatExited(t *testing.T) {
	pool, started := testPool(t)

	if _, err := ask(pool, context.Background(), "first"); err != nil {
		t.Fatal(err)
	}

	dead := pool.idle[0].(*Stdio)
	dead.cmd.Process.Kill()

	for !dead.Exited() {
		time.Sleep(time.Millisecond)
	}

	if answer, err := ask(pool, context.Background(), "second"); err != nil || answer != "second" || started.Load() != 2 {
		t.Fatalf("answer = %q, err = %v, started %d", answer, err, started.Load())
	}
}

// A closed pool stops its idle processes, stops a busy one once its request
// ends, and takes no more requests.
func TestPoolClose(t *testing.T) {
	pool, _ := testPool(t)

	if _, err := ask(pool, context.Background(), "first"); err != nil {
		t.Fatal(err)
	}

	idle := pool.idle[0].(*Stdio)
	pool.Close()

	for !idle.Exited() {
		time.Sleep(time.Millisecond)
	}

	if _, err := ask(pool, context.Background(), "late"); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("a request after close: err = %v", err)
	}
}

// A process that dies during a request fails it as transient, which the
// loop retries, and the retry goes to a new process.
func TestAProcessThatDiesFailsItsRequestAsTransient(t *testing.T) {
	pool, started := testPool(t)

	if err := pool.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}

	dying := pool.idle[0].(*Stdio)
	failed := make(chan error, 1)

	go func() {
		_, err := ask(pool, context.Background(), "hang")
		failed <- err
	}()

	// The request has taken the process once the pool has none idle.
	for {
		pool.mu.Lock()
		taken := len(pool.idle) == 0
		pool.mu.Unlock()

		if taken {
			break
		}

		time.Sleep(time.Millisecond)
	}

	dying.cmd.Process.Kill()

	if err := <-failed; !protocol.IsTransient(err) {
		t.Fatalf("the request of a process that died: err = %v", err)
	}

	if answer, err := ask(pool, context.Background(), "again"); err != nil || answer != "again" || started.Load() != 2 {
		t.Fatalf("answer = %q, err = %v, started %d", answer, err, started.Load())
	}
}
