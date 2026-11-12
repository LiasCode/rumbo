package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"rumbo/internal/store"
	"rumbo/internal/tool"
	"rumbo/protocol"
)

func testHost(t *testing.T) *host {
	t.Helper()

	database, err := store.Open(filepath.Join(t.TempDir(), "test.db"))

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { database.Close() })

	database.PutProgram(store.KindProvider, store.Program{Name: "first", Command: []string{"first-provider"}})
	database.PutProgram(store.KindProvider, store.Program{Name: "second", Command: []string{"second-provider"}})
	database.SetSetting(store.GlobalScope, "options.second", `{"depth":2}`)

	created := &host{
		database: database,
		root:     "/repo",
		tools:    tool.NewSet(),
		settings: config{Provider: "first", Model: "small", Options: []byte(`{"effort":"high"}`)},
		info:     protocol.FrontendInitializeParams{Dir: "/repo", Provider: "first", Model: "small"},
	}

	t.Cleanup(created.release)

	return created
}

func call(t *testing.T, h *host, name, params string) protocol.HostResult {
	t.Helper()

	result, err := h.invokeHost(context.Background(), name, []byte(params))

	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}

	return result
}

func TestHostFunctionsChangeSessionAndProvider(t *testing.T) {
	h := testHost(t)
	older, _ := h.database.CreateSession("/repo/sub", "/repo", "older work")
	h.database.CreateSession("/elsewhere", "/elsewhere", "another project")
	foreign, _ := h.database.Sessions("/elsewhere")

	listed := call(t, h, "sessions", "")

	if len(listed.Values) != 1 || listed.Values[0].Value != older.ID || listed.Values[0].Label != "older work" {
		t.Fatalf("sessions = %+v", listed.Values)
	}

	// Opening a session attaches the front end to it and to where it works.
	opened := call(t, h, "open", `{"session":"`+older.ID+`"}`)

	if opened.State.Session != older.ID || opened.State.Dir != "/repo/sub" || h.session == nil {
		t.Fatalf("after open: %+v", opened.State)
	}

	if _, err := h.database.LockSession(older.ID); err == nil {
		t.Fatal("the opened session is not held by this host")
	}

	if _, err := h.invokeHost(context.Background(), "open", []byte(`{"session":"`+foreign[0].ID+`"}`)); err == nil || !strings.Contains(err.Error(), "belongs to /elsewhere") {
		t.Fatalf("opening a session of another project: err = %v", err)
	}

	if status := call(t, h, "status", ""); !strings.Contains(status.Text, "session: "+older.ID) || !strings.Contains(status.Text, "context: 0 tokens") {
		t.Fatalf("status = %q", status.Text)
	}

	if fresh := call(t, h, "new", ""); fresh.State.Session != "" || h.session != nil {
		t.Fatalf("after new: %+v", fresh.State)
	}

	if unlock, err := h.database.LockSession(older.ID); err != nil {
		t.Fatal("the session left behind is still held")
	} else {
		unlock()
	}

	// Deleting removes the session and its history, and nothing else.
	kept, _ := h.database.CreateSession("/repo", "/repo", "kept")
	h.database.Append(older.ID, "user", map[string]string{"text": "hi"})

	if _, err := h.invokeHost(context.Background(), "delete", []byte(`{"session":"`+foreign[0].ID+`"}`)); err == nil {
		t.Fatal("a session of another project was deleted")
	}

	held, _ := h.database.LockSession(kept.ID)

	if _, err := h.invokeHost(context.Background(), "delete", []byte(`{"session":"`+kept.ID+`"}`)); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("deleting a session in use elsewhere: err = %v", err)
	}

	held()

	if deleted := call(t, h, "delete", `{"session":"`+older.ID+`"}`); !strings.Contains(deleted.Text, "older work") {
		t.Fatalf("delete = %q", deleted.Text)
	}

	if entries, _ := h.database.Entries(older.ID); len(entries) != 0 {
		t.Fatal("the entries of a deleted session are still there")
	}

	if remaining := call(t, h, "sessions", ""); len(remaining.Values) != 1 || remaining.Values[0].Value != kept.ID {
		t.Fatalf("after delete: %+v", remaining.Values)
	}

	// Deleting the session in use leaves the front end without one.
	call(t, h, "open", `{"session":"`+kept.ID+`"}`)

	if gone := call(t, h, "delete", `{"session":"`+kept.ID+`"}`); gone.State.Session != "" || h.session != nil {
		t.Fatalf("after deleting the open session: %+v", gone.State)
	}

	if providers := call(t, h, "providers", ""); len(providers.Values) != 2 || providers.Values[1].Value != "second" {
		t.Fatalf("providers = %+v", providers.Values)
	}

	// Changing provider brings that provider's stored options, not the ones
	// in force for the one before.
	used := call(t, h, "use", `{"provider":"second","model":"large"}`)

	if used.State.Provider != "second" || used.State.Model != "large" || string(h.settings.Options) != `{"depth":2}` {
		t.Fatalf("after use: %+v, options %s", used.State, h.settings.Options)
	}

	for name, params := range map[string]string{"use": `{"provider":"missing","model":"x"}`, "open": `{}`, "nope": ``} {
		if _, err := h.invokeHost(context.Background(), name, []byte(params)); err == nil {
			t.Fatalf("%s %s should fail", name, params)
		}
	}
}
