package hooks

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReadLabelsPreAndPost(t *testing.T) {
	labels := map[string]string{
		"sentinel.hook.pre-update":  "pg_dump -f /backup/db.sql",
		"sentinel.hook.post-update": "pg_restore /backup/db.sql",
		"sentinel.hook.timeout":     "60",
	}

	hooks := ReadLabels("postgres", labels)
	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hooks))
	}

	var pre, post *Hook
	for i := range hooks {
		switch hooks[i].Phase {
		case "pre-update":
			pre = &hooks[i]
		case "post-update":
			post = &hooks[i]
		}
	}

	if pre == nil {
		t.Fatal("missing pre-update hook")
	}
	if pre.Timeout != 60 {
		t.Errorf("expected timeout 60, got %d", pre.Timeout)
	}
	if pre.Command[2] != "pg_dump -f /backup/db.sql" {
		t.Errorf("unexpected pre command: %v", pre.Command)
	}

	if post == nil {
		t.Fatal("missing post-update hook")
	}
	if post.Command[2] != "pg_restore /backup/db.sql" {
		t.Errorf("unexpected post command: %v", post.Command)
	}
}

func TestReadLabelsEmpty(t *testing.T) {
	hooks := ReadLabels("nginx", map[string]string{})
	if len(hooks) != 0 {
		t.Errorf("expected 0 hooks, got %d", len(hooks))
	}
}

func TestHookLabelsRoundTrip(t *testing.T) {
	original := []Hook{
		{ContainerName: "postgres", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "pg_dump"}, Timeout: 60},
		{ContainerName: "postgres", Phase: "post-update", Command: []string{"/bin/sh", "-c", "pg_restore"}, Timeout: 30},
	}

	labels := HookLabels(original)
	roundTripped := ReadLabels("postgres", labels)

	if len(roundTripped) != 2 {
		t.Fatalf("expected 2 hooks after round-trip, got %d", len(roundTripped))
	}

	for _, h := range roundTripped {
		if h.Phase == "pre-update" && h.Command[2] != "pg_dump" {
			t.Errorf("pre-update command mismatch: %v", h.Command)
		}
		if h.Phase == "post-update" && h.Command[2] != "pg_restore" {
			t.Errorf("post-update command mismatch: %v", h.Command)
		}
	}
}

func TestDefaultTimeout(t *testing.T) {
	labels := map[string]string{
		"sentinel.hook.pre-update": "echo hello",
	}
	hooks := ReadLabels("test", labels)
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0].Timeout != 30 {
		t.Errorf("expected default timeout 30, got %d", hooks[0].Timeout)
	}
}

func TestHookLabelsShellCommandIsUnwrapped(t *testing.T) {
	labels := HookLabels([]Hook{
		{Phase: "pre-update", Command: []string{"/bin/sh", "-c", "pg_dump -U postgres app > /b.sql"}},
	})
	if got := labels["sentinel.hook.pre-update"]; got != "pg_dump -U postgres app > /b.sql" {
		t.Errorf("label = %q, want the raw shell command", got)
	}
}

func TestHookLabelsQuotesOtherArgv(t *testing.T) {
	labels := HookLabels([]Hook{
		{Phase: "post-update", Command: []string{"/usr/bin/curl", "-sf", "http://localhost/health?a=1&b=2"}},
	})
	want := "/usr/bin/curl -sf 'http://localhost/health?a=1&b=2'"
	if got := labels["sentinel.hook.post-update"]; got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
}

// TestHookLabelsArgvRoundTripThroughShell runs the written label through
// /bin/sh -c, as ReadLabels would, and checks the shell sees the same argv.
func TestHookLabelsArgvRoundTripThroughShell(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	argv := []string{"printf", "<%s>", "a b", "it's", "$HOME", "", "semi;colon", `back\slash`, "new\nline", "*", "~"}
	labels := HookLabels([]Hook{{Phase: "pre-update", Command: argv}})

	hooks := ReadLabels("app", labels)
	if len(hooks) != 1 || len(hooks[0].Command) != 3 || hooks[0].Command[0] != "/bin/sh" || hooks[0].Command[1] != "-c" {
		t.Fatalf("expected one /bin/sh -c hook back, got %+v", hooks)
	}
	out, err := exec.Command("/bin/sh", "-c", hooks[0].Command[2]).Output() //nolint:gosec // runs the fixed argv built above
	if err != nil {
		t.Fatalf("run %q: %v", hooks[0].Command, err)
	}
	want := "<a b><it's><$HOME><><semi;colon><back\\slash><new\nline><*><~>"
	if string(out) != want {
		t.Errorf("shell saw %q, want %q", out, want)
	}
}

func TestLabelCommandQuotesCommandWord(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"plain command", []string{"/usr/bin/curl", "-sf"}, "/usr/bin/curl -sf"},
		{"assignment-like command", []string{"FOO=bar", "x"}, "'FOO=bar' x"},
		{"equals in a path", []string{"/opt/a=b/run"}, "'/opt/a=b/run'"},
		{"equals in a later arg", []string{"/usr/bin/env", "FOO=bar", "run"}, "/usr/bin/env FOO=bar run"},
		{"reserved word in a later arg", []string{"echo", "if", "then"}, "echo if then"},
		{"near miss is not reserved", []string{"iff"}, "iff"},
		{"if", []string{"if", "x"}, "'if' x"},
		{"then", []string{"then"}, "'then'"},
		{"else", []string{"else"}, "'else'"},
		{"elif", []string{"elif"}, "'elif'"},
		{"fi", []string{"fi"}, "'fi'"},
		{"do", []string{"do"}, "'do'"},
		{"done", []string{"done"}, "'done'"},
		{"case", []string{"case"}, "'case'"},
		{"esac", []string{"esac"}, "'esac'"},
		{"while", []string{"while"}, "'while'"},
		{"until", []string{"until"}, "'until'"},
		{"for", []string{"for"}, "'for'"},
		{"in", []string{"in"}, "'in'"},
		{"function", []string{"function"}, "'function'"},
		{"time", []string{"time"}, "'time'"},
		{"bang", []string{"!"}, "'!'"},
		{"open brace", []string{"{"}, "'{'"},
		{"close brace", []string{"}"}, "'}'"},
		{"open test", []string{"[["}, "'[['"},
		{"close test", []string{"]]"}, "']]'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := labelCommand(tt.argv); got != tt.want {
				t.Errorf("labelCommand(%q) = %q, want %q", tt.argv, got, tt.want)
			}
		})
	}
}

// TestHookLabelsCommandWordRoundTripThroughShell puts an executable named
// after argv[0] on PATH and checks that /bin/sh -c, given the written label,
// runs it with the same arguments instead of reading an assignment or a
// reserved word.
func TestHookLabelsCommandWordRoundTripThroughShell(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh not available")
	}
	for _, argv0 := range []string{"FOO=bar", "if"} {
		t.Run(argv0, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf '<%s>' \"${0##*/}\" \"$@\"\n"
			if err := os.WriteFile(dir+"/"+argv0, []byte(script), 0o755); err != nil { //nolint:gosec // test executable
				t.Fatal(err)
			}

			labels := HookLabels([]Hook{{Phase: "pre-update", Command: []string{argv0, "a b"}}})
			hooks := ReadLabels("app", labels)
			if len(hooks) != 1 {
				t.Fatalf("expected one hook back, got %+v", hooks)
			}
			cmd := exec.Command("/bin/sh", "-c", hooks[0].Command[2]) //nolint:gosec // runs the fixed argv built above
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("run %q: %v: %s", hooks[0].Command[2], err, out)
			}
			if want := "<" + argv0 + "><a b>"; string(out) != want {
				t.Errorf("shell ran %q as %q, want %q", hooks[0].Command[2], out, want)
			}
		})
	}
}

func TestHookLabelsTimeoutUsesLongestHook(t *testing.T) {
	labels := HookLabels([]Hook{
		{Phase: "pre-update", Command: []string{"/bin/sh", "-c", "a"}, Timeout: 120},
		{Phase: "post-update", Command: []string{"/bin/sh", "-c", "b"}, Timeout: 60},
	})
	if got := labels["sentinel.hook.timeout"]; got != "120" {
		t.Errorf("timeout label = %q, want 120 so neither hook is cut short", got)
	}
}

func TestHookLabelsDefaultTimeoutOmitted(t *testing.T) {
	labels := HookLabels([]Hook{{Phase: "pre-update", Command: []string{"/bin/sh", "-c", "a"}, Timeout: 30}})
	if _, ok := labels["sentinel.hook.timeout"]; ok {
		t.Errorf("default timeout should not be written, got %v", labels)
	}
}

func TestWriteBackLabelsStoreWins(t *testing.T) {
	store := newMockStore()
	store.hooks["app"] = []Hook{
		{ContainerName: "app", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "new backup"}, Timeout: 90},
	}
	labels := map[string]string{
		"sentinel.hook.pre-update": "old backup",
		"sentinel.hook.timeout":    "10",
		"com.example.other":        "kept",
	}

	var buf bytes.Buffer
	runner := NewRunner(newMockExec(), store, captureLog(&buf))
	got, err := runner.WriteBackLabels("app", labels)
	if err != nil {
		t.Fatalf("WriteBackLabels: %v", err)
	}
	if got["sentinel.hook.pre-update"] != "new backup" {
		t.Errorf("pre-update label = %q, want the stored command", got["sentinel.hook.pre-update"])
	}
	if got["sentinel.hook.timeout"] != "90" {
		t.Errorf("timeout label = %q, want 90", got["sentinel.hook.timeout"])
	}
	if got["com.example.other"] != "kept" {
		t.Error("unrelated labels must be kept")
	}
	if !strings.Contains(buf.String(), "level=DEBUG") || !strings.Contains(buf.String(), "sentinel.hook.pre-update") {
		t.Errorf("expected a debug line about the replaced label, got:\n%s", buf.String())
	}
}

func TestWriteBackLabelsResetsTimeoutToStoreDefault(t *testing.T) {
	store := newMockStore()
	store.hooks["app"] = []Hook{
		{ContainerName: "app", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "a"}, Timeout: 30},
	}
	labels := map[string]string{"sentinel.hook.timeout": "300"}

	runner := NewRunner(newMockExec(), store, slog.Default())
	got, err := runner.WriteBackLabels("app", labels)
	if err != nil {
		t.Fatalf("WriteBackLabels: %v", err)
	}
	back := ReadLabels("app", got)
	if len(back) != 1 || back[0].Timeout != 30 {
		t.Errorf("labels %v read back as %+v, want the stored 30s timeout", got, back)
	}
}

func TestWriteBackLabelsKeepsTimeoutOfLabelOnlyPhase(t *testing.T) {
	store := newMockStore()
	store.hooks["app"] = []Hook{
		{ContainerName: "app", Phase: "pre-update", Command: []string{"/bin/sh", "-c", "a"}, Timeout: 30},
	}
	// The post-update hook still comes from labels and relies on this timeout.
	labels := map[string]string{
		"sentinel.hook.post-update": "warm cache",
		"sentinel.hook.timeout":     "300",
	}

	runner := NewRunner(newMockExec(), store, slog.Default())
	got, err := runner.WriteBackLabels("app", labels)
	if err != nil {
		t.Fatalf("WriteBackLabels: %v", err)
	}
	if got["sentinel.hook.timeout"] != "300" {
		t.Errorf("timeout label = %q, want 300 left alone for the label-only post-update hook", got["sentinel.hook.timeout"])
	}
	if got["sentinel.hook.pre-update"] != "a" || got["sentinel.hook.post-update"] != "warm cache" {
		t.Errorf("unexpected labels %v", got)
	}
}

func TestWriteBackLabelsNoStoredHooks(t *testing.T) {
	labels := map[string]string{"sentinel.hook.pre-update": "from compose"}
	runner := NewRunner(newMockExec(), newMockStore(), slog.Default())
	got, err := runner.WriteBackLabels("app", labels)
	if err != nil {
		t.Fatalf("WriteBackLabels: %v", err)
	}
	if len(got) != 1 || got["sentinel.hook.pre-update"] != "from compose" {
		t.Errorf("labels changed without stored hooks: %v", got)
	}
}

func TestWriteBackLabelsNilMap(t *testing.T) {
	store := newMockStore()
	store.hooks["app"] = []Hook{
		{ContainerName: "app", Phase: "post-update", Command: []string{"/bin/sh", "-c", "b"}},
	}
	runner := NewRunner(newMockExec(), store, slog.Default())
	got, err := runner.WriteBackLabels("app", nil)
	if err != nil {
		t.Fatalf("WriteBackLabels: %v", err)
	}
	if got["sentinel.hook.post-update"] != "b" {
		t.Errorf("labels = %v, want the stored post-update hook", got)
	}
}
