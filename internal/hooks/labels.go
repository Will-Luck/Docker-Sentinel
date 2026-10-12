package hooks

import (
	"fmt"
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

// HookLabels converts hooks to container labels for portability.
func HookLabels(hooks []Hook) map[string]string {
	labels := make(map[string]string)
	for _, h := range hooks {
		cmd := strings.Join(h.Command, " ")
		// If command is ["/bin/sh", "-c", "actual command"], extract the actual command.
		if len(h.Command) == 3 && h.Command[0] == "/bin/sh" && h.Command[1] == "-c" {
			cmd = h.Command[2]
		}
		switch h.Phase {
		case PhasePreUpdate:
			labels[labelPreUpdate] = cmd
		case PhasePostUpdate:
			labels[labelPostUpdate] = cmd
		}
		if h.Timeout > 0 && h.Timeout != DefaultTimeout {
			labels[labelTimeout] = fmt.Sprintf("%d", h.Timeout)
		}
	}
	return labels
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
