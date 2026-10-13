package web

// effectiveRollbackPolicy returns the rollback policy the engine uses: the
// runtime config, which holds the environment default until a value saved in
// the UI replaces it. Without a config it falls back to the saved value.
func (s *Server) effectiveRollbackPolicy() string {
	if s.deps.Config != nil {
		return s.deps.Config.RollbackPolicy()
	}
	if s.deps.SettingsStore != nil {
		if rp, err := s.deps.SettingsStore.LoadSetting("rollback_policy"); err == nil {
			return rp
		}
	}
	return ""
}
