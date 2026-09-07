module github.com/easylab-platform/pkr/cmd-pkr

go 1.26.5

require (
	github.com/easylab-platform/pkr/pkr-cargo v0.0.0
	github.com/easylab-platform/pkr/pkr-composer v0.0.0
	github.com/easylab-platform/pkr/pkr-conan v0.0.0
	github.com/easylab-platform/pkr/pkr-generic v0.0.0
	github.com/easylab-platform/pkr/pkr-go v0.0.0
	github.com/easylab-platform/pkr/pkr-helm v0.0.0
	github.com/easylab-platform/pkr/pkr-hex v0.0.0
	github.com/easylab-platform/pkr/pkr-maven v0.0.0
	github.com/easylab-platform/pkr/pkr-npm v0.0.0
	github.com/easylab-platform/pkr/pkr-nuget v0.0.0
	github.com/easylab-platform/pkr/pkr-oci v0.0.0
	github.com/easylab-platform/pkr/pkr-pub v0.0.0
	github.com/easylab-platform/pkr/pkr-pypi v0.0.0
	github.com/easylab-platform/pkr/pkr-rubygems v0.0.0
	github.com/easylab-platform/pkr/pkr-swift v0.0.0
	github.com/easylab-platform/pkr/pkr-system v0.0.0
	github.com/easylab-platform/pkr/pkrkit v0.0.0
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
	github.com/easylab-platform/pkr/pkr-cargo => ../pkr-cargo
	github.com/easylab-platform/pkr/pkr-composer => ../pkr-composer
	github.com/easylab-platform/pkr/pkr-conan => ../pkr-conan
	github.com/easylab-platform/pkr/pkr-generic => ../pkr-generic
	github.com/easylab-platform/pkr/pkr-go => ../pkr-go
	github.com/easylab-platform/pkr/pkr-helm => ../pkr-helm
	github.com/easylab-platform/pkr/pkr-hex => ../pkr-hex
	github.com/easylab-platform/pkr/pkr-maven => ../pkr-maven
	github.com/easylab-platform/pkr/pkr-npm => ../pkr-npm
	github.com/easylab-platform/pkr/pkr-nuget => ../pkr-nuget
	github.com/easylab-platform/pkr/pkr-oci => ../pkr-oci
	github.com/easylab-platform/pkr/pkr-pub => ../pkr-pub
	github.com/easylab-platform/pkr/pkr-pypi => ../pkr-pypi
	github.com/easylab-platform/pkr/pkr-rubygems => ../pkr-rubygems
	github.com/easylab-platform/pkr/pkr-swift => ../pkr-swift
	github.com/easylab-platform/pkr/pkr-system => ../pkr-system
	github.com/easylab-platform/pkr/pkrkit => ../pkrkit
)
