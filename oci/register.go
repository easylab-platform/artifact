package oci

import "github.com/easylab-platform/artifact/core"

func init() {
	artifactkit.Register("oci", NewHandler)
}
