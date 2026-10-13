package docker

import cerrdefs "github.com/containerd/errdefs"

// IsNotFound reports whether err says the container, image or other object
// does not exist, as opposed to a failure to reach or ask the daemon.
func IsNotFound(err error) bool {
	return cerrdefs.IsNotFound(err)
}
