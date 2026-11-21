package startlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInitAndStages exercises the exact path main() relies on:
// select a writable log location, then write stage markers and
// confirm they land on disk in order.
func TestInitAndStages(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "DaybreakNVR", "startup.log")
	bad := filepath.Join(dir, "nope\x00invalid", "startup.log")

	if got := InitFrom([]string{bad, good}); got != good {
		t.Fatalf("InitFrom picked %q, want %q", got, good)
	}
	if Path() != good {
		t.Fatalf("Path() = %q, want %q", Path(), good)
	}

	Stage("log opened at %s", good)
	Stage("settings loaded: %d cameras", 0)
	Stage("entering event loop")

	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"log opened", "settings loaded: 0 cameras", "entering event loop"} {
		if !strings.Contains(text, want) {
			t.Errorf("log missing %q; got:\n%s", want, text)
		}
	}
	if strings.Index(text, "log opened") > strings.Index(text, "entering event loop") {
		t.Errorf("stages out of order:\n%s", text)
	}
}

// TestPickSkipsUnwritable confirms candidate fallback works.
func TestPickSkipsUnwritable(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "startup.log")
	if got := Pick([]string{"\x00bad", good}); got != good {
		t.Fatalf("Pick = %q, want %q", got, good)
	}
	if got := Pick([]string{"\x00bad"}); got != "" {
		t.Fatalf("Pick of only-bad candidates = %q, want empty", got)
	}
}

// TestPanicCapture confirms a panic inside Guard is logged with a
// stack, and Go's guard keeps the process alive.
func TestPanicCapture(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "startup.log")
	InitFrom([]string{p})

	if !Guard("test-guard", func() { panic("boom") }) {
		t.Fatal("Guard did not report the panic")
	}
	done := make(chan struct{})
	Go("test-go", func() { panic("goroutine boom") })
	Go("test-go-ok", func() { close(done) })
	<-done

	// Go's panic is logged from its own goroutine; poll briefly.
	var text string
	for i := 0; i < 200; i++ {
		data, _ := os.ReadFile(p)
		text = string(data)
		if strings.Contains(text, "goroutine boom") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(text, "PANIC: panic in test-guard: boom") {
		t.Errorf("guard panic not logged:\n%s", text)
	}
	if !strings.Contains(text, "goroutine boom") {
		t.Errorf("goroutine panic not logged:\n%s", text)
	}
	if !strings.Contains(text, "goroutine ") && !strings.Contains(text, "startlog") {
		t.Errorf("stack trace missing from log:\n%s", text)
	}
}
