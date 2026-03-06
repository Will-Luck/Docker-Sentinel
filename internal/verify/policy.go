package verify

// ContainerLabel is the Docker label for per-container verification mode.
const ContainerLabel = "sentinel.verify"

// ResolveMode determines the effective verification mode for a container.
// Priority: container label > per-container config > global default.
func ResolveMode(label string, perContainer Mode, global Mode) Mode {
	if label != "" {
		m := ParseMode(label)
		if m != ModeDisabled || label == "disabled" {
			return m
		}
	}
	if perContainer != "" && perContainer != ModeDisabled {
		return perContainer
	}
	return global
}
