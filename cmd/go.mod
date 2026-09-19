module github.com/easylab-platform/artifact/cmd

go 1.26.5

require (
	github.com/easylab-platform/artifact/apk v0.0.0
	github.com/easylab-platform/artifact/cargo v0.0.0
	github.com/easylab-platform/artifact/composer v0.0.0
	github.com/easylab-platform/artifact/conan v0.0.0
	github.com/easylab-platform/artifact/conda v0.0.0
	github.com/easylab-platform/artifact/core v0.0.0
	github.com/easylab-platform/artifact/debian v0.0.0
	github.com/easylab-platform/artifact/generic v0.0.0
	github.com/easylab-platform/artifact/git v0.0.0
	github.com/easylab-platform/artifact/gitlfs v0.0.0
	github.com/easylab-platform/artifact/go v0.0.0
	github.com/easylab-platform/artifact/helm v0.0.0
	github.com/easylab-platform/artifact/hex v0.0.0
	github.com/easylab-platform/artifact/httpcache v0.0.0
	github.com/easylab-platform/artifact/huggingface v0.0.0
	github.com/easylab-platform/artifact/ivy v0.0.0
	github.com/easylab-platform/artifact/maven v0.0.0
	github.com/easylab-platform/artifact/nix v0.0.0
	github.com/easylab-platform/artifact/npm v0.0.0
	github.com/easylab-platform/artifact/nuget v0.0.0
	github.com/easylab-platform/artifact/oci v0.0.0
	github.com/easylab-platform/artifact/protobuf v0.0.0
	github.com/easylab-platform/artifact/pub v0.0.0
	github.com/easylab-platform/artifact/pypi v0.0.0
	github.com/easylab-platform/artifact/rpm v0.0.0
	github.com/easylab-platform/artifact/rubygems v0.0.0
	github.com/easylab-platform/artifact/swiftpm v0.0.0
	github.com/easylab-platform/artifact/system v0.0.0
	github.com/easylab-platform/artifact/targets v0.1.0
)

require (
	filippo.io/edwards25519 v1.1.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/easylab-platform/artifact/netcache v0.0.0
	github.com/glebarez/go-sqlite v1.21.2 // indirect
	github.com/glebarez/sqlite v1.11.0 // indirect
	github.com/go-sql-driver/mysql v1.8.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/ulikunitz/xz v0.5.16 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.29.0 // indirect
	gorm.io/driver/mysql v1.6.0 // indirect
	gorm.io/driver/postgres v1.6.2 // indirect
	gorm.io/gorm v1.31.2 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.58.0 // indirect
)

replace (
	github.com/easylab-platform/artifact/apk => ../apk
	github.com/easylab-platform/artifact/cargo => ../cargo
	github.com/easylab-platform/artifact/composer => ../composer
	github.com/easylab-platform/artifact/conan => ../conan
	github.com/easylab-platform/artifact/conda => ../conda
	github.com/easylab-platform/artifact/core => ../core
	github.com/easylab-platform/artifact/debian => ../debian
	github.com/easylab-platform/artifact/generic => ../generic
	github.com/easylab-platform/artifact/git => ../git
	github.com/easylab-platform/artifact/gitlfs => ../gitlfs
	github.com/easylab-platform/artifact/go => ../go
	github.com/easylab-platform/artifact/helm => ../helm
	github.com/easylab-platform/artifact/hex => ../hex
	github.com/easylab-platform/artifact/httpcache => ../httpcache
	github.com/easylab-platform/artifact/huggingface => ../huggingface
	github.com/easylab-platform/artifact/ivy => ../ivy
	github.com/easylab-platform/artifact/maven => ../maven
	github.com/easylab-platform/artifact/nix => ../nix
	github.com/easylab-platform/artifact/npm => ../npm
	github.com/easylab-platform/artifact/nuget => ../nuget
	github.com/easylab-platform/artifact/oci => ../oci
	github.com/easylab-platform/artifact/protobuf => ../protobuf
	github.com/easylab-platform/artifact/pub => ../pub
	github.com/easylab-platform/artifact/pypi => ../pypi
	github.com/easylab-platform/artifact/rpm => ../rpm
	github.com/easylab-platform/artifact/rubygems => ../rubygems
	github.com/easylab-platform/artifact/swiftpm => ../swiftpm
	github.com/easylab-platform/artifact/system => ../system
	github.com/easylab-platform/artifact/targets => ../targets
)

replace github.com/easylab-platform/artifact/netcache => ../netcache
