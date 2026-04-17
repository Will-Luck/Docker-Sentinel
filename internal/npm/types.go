package npm

import "encoding/json"

// ProxyHost represents an NPM proxy host entry.
type ProxyHost struct {
	ID            int      `json:"id"`
	DomainNames   []string `json:"domain_names"`
	ForwardScheme string   `json:"forward_scheme"`
	ForwardHost   string   `json:"forward_host"`
	ForwardPort   int      `json:"forward_port"`
	Enabled       flexBool `json:"enabled"`
	CertificateID int      `json:"certificate_id"`
}

// flexBool handles NPM's "enabled" field which is bool in some versions and
// int (0/1) in others.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	// Try bool first.
	var boolVal bool
	if err := json.Unmarshal(data, &boolVal); err == nil {
		*b = flexBool(boolVal)
		return nil
	}
	// Fall back to int (0 = false, anything else = true).
	var intVal int
	if err := json.Unmarshal(data, &intVal); err == nil {
		*b = flexBool(intVal != 0)
		return nil
	}
	// Default to false.
	*b = false
	return nil
}

// ResolvedURL is a URL resolved from an NPM proxy host match.
type ResolvedURL struct {
	URL         string // full URL (e.g. "https://radarr.example.com")
	Domain      string // primary domain (first entry)
	ProxyHostID int    // NPM proxy host ID for reference
}
