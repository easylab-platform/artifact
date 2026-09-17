package oci

import "github.com/easylab-platform/artifact/core"

func init() {
	artifactkit.Register("oci", NewHandler)
	// The OCI repository's upstream is decided per registry host
	// (ghcr.io/quay.io/...), so expose the host as the namespace. Requests
	// without a host prefix keep the default upstream.
	artifactkit.RegisterNamespace("oci", artifactkit.OCIHostNamespace)
}
