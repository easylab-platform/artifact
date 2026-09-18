module github.com/easylab-platform/artifact/system

go 1.26.5

require (
	github.com/easylab-platform/artifact/core v0.0.0
	github.com/easylab-platform/artifact/targets v0.1.0
)

replace github.com/easylab-platform/artifact/core => ../core

replace github.com/easylab-platform/artifact/targets => ../targets
