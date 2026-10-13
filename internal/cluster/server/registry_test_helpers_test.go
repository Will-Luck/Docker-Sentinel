package server

// pendingSerial returns the host's pending renewal serial.
func (r *Registry) pendingSerial(hostID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if hs, ok := r.hosts[hostID]; ok {
		return hs.Info.PendingCertSerial
	}
	return ""
}
