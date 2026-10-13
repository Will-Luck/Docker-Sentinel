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
