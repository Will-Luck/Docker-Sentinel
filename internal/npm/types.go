package npm

// ProxyHost represents an NPM proxy host entry.
type ProxyHost struct {
	ID            int      `json:"id"`
	DomainNames   []string `json:"domain_names"`
	ForwardScheme string   `json:"forward_scheme"`
	ForwardHost   string   `json:"forward_host"`
	ForwardPort   int      `json:"forward_port"`
	Enabled       int      `json:"enabled"`
	CertificateID int      `json:"certificate_id"`
}

// ResolvedURL is a URL resolved from an NPM proxy host match.
type ResolvedURL struct {
	URL         string // full URL (e.g. "https://radarr.lucknet.uk")
	Domain      string // primary domain (first entry)
	ProxyHostID int    // NPM proxy host ID for reference
}
