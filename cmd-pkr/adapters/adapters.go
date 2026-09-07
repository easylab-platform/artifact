// Package adapters blank-imports third-party protocol adapter packages so
// their Register() calls (via init) are pulled into the binary. To enable a
// protocol, add an import here and rebuild.
package adapters

import (
	_ "github.com/easylab-platform/pkr/pkr-cargo"
	_ "github.com/easylab-platform/pkr/pkr-composer"
	_ "github.com/easylab-platform/pkr/pkr-conan"
	_ "github.com/easylab-platform/pkr/pkr-generic"
	_ "github.com/easylab-platform/pkr/pkr-go"
	_ "github.com/easylab-platform/pkr/pkr-helm"
	_ "github.com/easylab-platform/pkr/pkr-hex"
	_ "github.com/easylab-platform/pkr/pkr-maven"
	_ "github.com/easylab-platform/pkr/pkr-npm"
	_ "github.com/easylab-platform/pkr/pkr-nuget"
	_ "github.com/easylab-platform/pkr/pkr-oci"
	_ "github.com/easylab-platform/pkr/pkr-system"
	_ "github.com/easylab-platform/pkr/pkr-pub"
	_ "github.com/easylab-platform/pkr/pkr-pypi"
	_ "github.com/easylab-platform/pkr/pkr-rubygems"
	_ "github.com/easylab-platform/pkr/pkr-swift"
)
