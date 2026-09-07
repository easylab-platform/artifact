package oci

import "github.com/easylab-platform/pkr/pkrkit"

func init() {
	pkrkit.Register("oci", NewHandler)
}
