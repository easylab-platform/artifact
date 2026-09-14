module github.com/easylab-platform/artifact/debian

go 1.26.5

require github.com/easylab-platform/artifact/core v0.0.0

require github.com/ulikunitz/xz v0.5.16 // indirect

replace github.com/easylab-platform/artifact/core => ../core
