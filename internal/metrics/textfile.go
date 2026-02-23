package metrics

import (
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
)

// WriteTextfile writes current sentinel_ metrics in Prometheus exposition format
// to path, using an atomic write (temp file + rename).
// Intended for use with node_exporter's textfile collector.
func WriteTextfile(path string) error {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	enc := expfmt.NewEncoder(f, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if strings.HasPrefix(mf.GetName(), "sentinel_") {
			if encErr := enc.Encode(mf); encErr != nil {
				f.Close()
				os.Remove(tmp)
				return encErr
			}
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
