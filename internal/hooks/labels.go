package hooks

import (
	"strconv"
	"strings"
)

const (
	labelPreUpdate  = "sentinel.hook.pre-update"
	labelPostUpdate = "sentinel.hook.post-update"
	labelTimeout    = "sentinel.hook.timeout"
)

// phaseLabel returns the label key that holds the command for phase.
func phaseLabel(phase string) string {
	if phase == PhasePostUpdate {
		return labelPostUpdate
	}
	return labelPreUpdate
}

// ReadLabels reads hook config from container labels. Commands run as
// /bin/sh -c <value>. Empty values are ignored, and a missing or invalid
// timeout falls back to DefaultTimeout.
func ReadLabels(name string, labels map[string]string) []Hook {
	var hooks []Hook

	timeout := DefaultTimeout
	if v, ok := labels[labelTimeout]; ok {
		if t, valid := parseTimeout(v); valid {
			timeout = t
		}
	}

	if cmd, ok := labels[labelPreUpdate]; ok && !isBlank(cmd) {
		hooks = append(hooks, Hook{
			ContainerName: name,
			Phase:         PhasePreUpdate,
			Command:       []string{"/bin/sh", "-c", cmd},
			Timeout:       timeout,
		})
	}

	if cmd, ok := labels[labelPostUpdate]; ok && !isBlank(cmd) {
		hooks = append(hooks, Hook{
			ContainerName: name,
			Phase:         PhasePostUpdate,
			Command:       []string{"/bin/sh", "-c", cmd},
			Timeout:       timeout,
		})
	}

	return hooks
}

// HookLabels converts hooks to container labels that ReadLabels reads back as
// the same commands. A ["/bin/sh", "-c", X] command becomes the label X; any
// other argv becomes a POSIX shell-quoted join, which /bin/sh -c runs as the
// same argv. The single timeout label takes the longest hook timeout, so no
// hook is cut short, and is omitted when that is the default.
func HookLabels(hooks []Hook) map[string]string {
	labels := make(map[string]string)
	timeout := 0
	for _, h := range hooks {
		if len(h.Command) == 0 {
			continue
		}
		switch h.Phase {
		case PhasePreUpdate, PhasePostUpdate:
			labels[phaseLabel(h.Phase)] = labelCommand(h.Command)
		default:
			continue
		}
		timeout = max(timeout, effectiveTimeout(h.Timeout))
	}
	if timeout > 0 && timeout != DefaultTimeout {
		labels[labelTimeout] = strconv.Itoa(timeout)
	}
	return labels
}

// labelCommand returns the label value for a stored hook command.
func labelCommand(argv []string) string {
	if len(argv) == 3 && argv[0] == "/bin/sh" && argv[1] == "-c" {
		return argv[2]
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		if i == 0 {
			quoted[i] = quoteCommandWord(arg)
			continue
		}
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// shellReserved holds the words a shell treats as syntax rather than as a
// command name when they are unquoted in command position: the POSIX reserved
// words, plus function, time, select, coproc and [[ ]] from bash and ksh,
// where /bin/sh may be one of those.
var shellReserved = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "fi": true,
	"do": true, "done": true, "case": true, "esac": true, "while": true,
	"until": true, "for": true, "in": true, "function": true, "time": true,
	"select": true, "coproc": true,
	"!": true, "{": true, "}": true, "[[": true, "]]": true,
}

// quoteCommandWord quotes argv[0]. On top of what shellQuote quotes, it
// always quotes a word holding '=' (an unquoted FOO=bar is read as a variable
// assignment) or a reserved word (an unquoted if starts an if statement), so
// the shell runs it as the command.
func quoteCommandWord(s string) string {
	if strings.ContainsRune(s, '=') || shellReserved[s] {
		return singleQuote(s)
	}
	return shellQuote(s)
}

// shellQuote quotes s for a POSIX shell. Words made only of safe characters
// are left as they are; anything else is single-quoted.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !isShellSafe(r) {
			return singleQuote(s)
		}
	}
	return s
}

// singleQuote wraps s in single quotes, closing and escaping any it contains.
func singleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isShellSafe(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("@%+=:,./-_", r)
}

func effectiveTimeout(t int) int {
	if t <= 0 {
		return DefaultTimeout
	}
	return t
}

// parseTimeout parses a timeout label value in whole seconds. It reports false
// for anything that is not a positive integer, such as "2m" or "0".
func parseTimeout(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func isBlank(s string) bool {
	return strings.TrimSpace(s) == ""
}
