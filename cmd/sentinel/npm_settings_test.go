package main

import (
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
)

func TestNPMSettings_EnvFirstThenSaved(t *testing.T) {
	saved := map[string]string{"npm_url": "http://saved", "npm_email": "saved@x", "npm_password": "savedpw"}
	load := func(k string) (string, error) { return saved[k], nil }

	t.Setenv("SENTINEL_NPM_URL", "http://env")
	t.Setenv("SENTINEL_NPM_EMAIL", "")
	t.Setenv("SENTINEL_NPM_PASSWORD", "")
	u, e, p := npmSettings(config.Load(), load)
	if u != "http://env" || e != "saved@x" || p != "savedpw" {
		t.Errorf("got %q %q %q: env should win where set and saved fill the rest", u, e, p)
	}

	t.Setenv("SENTINEL_NPM_URL", "")
	if u, _, _ := npmSettings(config.Load(), load); u != "http://saved" {
		t.Errorf("url = %q, want the saved one when the environment has none", u)
	}
}
