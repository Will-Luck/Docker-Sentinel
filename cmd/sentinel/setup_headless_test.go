package main

import "testing"

func TestHeadlessFirstRunSkipsWizardAndOpensScanGate(t *testing.T) {
	if wizardRequired("", false) {
		t.Error("wizard required although the web UI is disabled")
	}
	if !wizardRequired("", true) {
		t.Error("wizard must still run on a fresh web install")
	}
	if wizardRequired("server", true) {
		t.Error("wizard must not run once a role is saved")
	}
	if !scanGateOpensAtStart(true, false) {
		t.Error("scan gate stays shut on a fresh headless install: scans would never start")
	}
	if scanGateOpensAtStart(true, true) {
		t.Error("fresh web install must hold scans until the dashboard loads")
	}
	if !scanGateOpensAtStart(false, true) {
		t.Error("configured install must scan at once")
	}
}

func TestHeadlessRoleChoice(t *testing.T) {
	cases := []struct {
		name, mode, token, addr, want string
	}{
		{"nothing set", "", "", "", "server"},
		{"explicit agent mode", "agent", "", "", "agent"},
		{"enrol token and server address", "", "tok", "hub:9443", "agent"},
		{"token without address", "", "tok", "", "server"},
		{"explicit server beats enrol vars", "server", "tok", "hub:9443", "server"},
	}
	for _, c := range cases {
		if got := headlessRole(c.mode, c.token, c.addr); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
