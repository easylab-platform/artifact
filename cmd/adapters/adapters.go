// Package adapters blank-imports third-party protocol adapter packages so
// their Register() calls (via init) are pulled into the binary. To enable a
// protocol, add an import here and rebuild.
package adapters

import (
	_ "github.com/easylab-platform/artifact/cargo"
	_ "github.com/easylab-platform/artifact/composer"
	_ "github.com/easylab-platform/artifact/conan"
	_ "github.com/easylab-platform/artifact/generic"
	_ "github.com/easylab-platform/artifact/go"
	_ "github.com/easylab-platform/artifact/helm"
	_ "github.com/easylab-platform/artifact/hex"
	_ "github.com/easylab-platform/artifact/maven"
	_ "github.com/easylab-platform/artifact/npm"
	_ "github.com/easylab-platform/artifact/nuget"
	_ "github.com/easylab-platform/artifact/oci"
	_ "github.com/easylab-platform/artifact/system"
	_ "github.com/easylab-platform/artifact/pub"
	_ "github.com/easylab-platform/artifact/pypi"
	_ "github.com/easylab-platform/artifact/rubygems"
	_ "github.com/easylab-platform/artifact/swiftpm"
)
