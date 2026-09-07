package oci

import "github.com/easylab-platform/artifact/core"

func init() {
	pkrkit.Register("oci", NewHandler)
}
