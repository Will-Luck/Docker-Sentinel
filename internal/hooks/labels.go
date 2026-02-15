package hooks

import (
	"fmt"
	"strings"
)

const (
	labelPreUpdate  = "sentinel.hook.pre-update"
	labelPostUpdate = "sentinel.hook.post-update"
	labelTimeout    = "sentinel.hook.timeout"
)

// ReadLabels reads hook config from container labels.
func ReadLabels(name string, labels map[string]string) []Hook {
	var hooks []Hook

	timeout := 30
	if v, ok := labels[labelTimeout]; ok {
		if t := parseTimeout(v); t > 0 {
			timeout = t
		}
	}

	if cmd, ok := labels[labelPreUpdate]; ok && cmd != "" {
		hooks = append(hooks, Hook{
			ContainerName: name,
			Phase:         "pre-update",
			Command:       []string{"/bin/sh", "-c", cmd},
			Timeout:       timeout,
		})
	}

	if cmd, ok := labels[labelPostUpdate]; ok && cmd != "" {
		hooks = append(hooks, Hook{
			ContainerName: name,
			Phase:         "post-update",
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
		case "pre-update":
			labels[labelPreUpdate] = cmd
		case "post-update":
			labels[labelPostUpdate] = cmd
		}
		if h.Timeout > 0 && h.Timeout != 30 {
			labels[labelTimeout] = fmt.Sprintf("%d", h.Timeout)
		}
	}
	return labels
}

func parseTimeout(s string) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n
}
