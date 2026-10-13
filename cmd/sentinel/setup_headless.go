package main

// wizardRequired reports whether the first-run setup wizard must run. With the
// web UI disabled there is nowhere to serve it, so it never does.
func wizardRequired(instanceRole string, webEnabled bool) bool {
	return instanceRole == "" && webEnabled
}

// scanGateOpensAtStart reports whether scans may start straight away. On a
// fresh setup they wait until the dashboard is first loaded, which can only
// happen when the web UI is running.
func scanGateOpensAtStart(freshSetup, webEnabled bool) bool {
	return !freshSetup || !webEnabled
}

// headlessRole picks the instance role for a first run with the web UI
// disabled. An explicit SENTINEL_MODE wins; otherwise the enrolment variables
// from the Cluster page snippet make it an agent, and anything else a server.
func headlessRole(mode, enrollToken, serverAddr string) string {
	switch {
	case mode != "":
		return mode
	case enrollToken != "" && serverAddr != "":
		return "agent"
	}
	return "server"
}
