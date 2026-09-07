module github.com/easylab-platform/artifact/cmd

go 1.26.5

require (
	github.com/easylab-platform/artifact/cargo v0.0.0
	github.com/easylab-platform/artifact/composer v0.0.0
	github.com/easylab-platform/artifact/conan v0.0.0
	github.com/easylab-platform/artifact/generic v0.0.0
	github.com/easylab-platform/artifact/go v0.0.0
	github.com/easylab-platform/artifact/helm v0.0.0
	github.com/easylab-platform/artifact/hex v0.0.0
	github.com/easylab-platform/artifact/maven v0.0.0
	github.com/easylab-platform/artifact/npm v0.0.0
	github.com/easylab-platform/artifact/nuget v0.0.0
	github.com/easylab-platform/artifact/oci v0.0.0
	github.com/easylab-platform/artifact/pub v0.0.0
	github.com/easylab-platform/artifact/pypi v0.0.0
	github.com/easylab-platform/artifact/rubygems v0.0.0
	github.com/easylab-platform/artifact/swiftpm v0.0.0
	github.com/easylab-platform/artifact/system v0.0.0
	github.com/easylab-platform/artifact/core v0.0.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.58.0 // indirect
)

replace (
	github.com/easylab-platform/artifact/cargo => ../cargo
	github.com/easylab-platform/artifact/composer => ../composer
	github.com/easylab-platform/artifact/conan => ../conan
	github.com/easylab-platform/artifact/generic => ../generic
	github.com/easylab-platform/artifact/go => ../go
	github.com/easylab-platform/artifact/helm => ../helm
	github.com/easylab-platform/artifact/hex => ../hex
	github.com/easylab-platform/artifact/maven => ../maven
	github.com/easylab-platform/artifact/npm => ../npm
	github.com/easylab-platform/artifact/nuget => ../nuget
	github.com/easylab-platform/artifact/oci => ../oci
	github.com/easylab-platform/artifact/pub => ../pub
	github.com/easylab-platform/artifact/pypi => ../pypi
	github.com/easylab-platform/artifact/rubygems => ../rubygems
	github.com/easylab-platform/artifact/swiftpm => ../swiftpm
	github.com/easylab-platform/artifact/system => ../system
	github.com/easylab-platform/artifact/core => ../core
)
