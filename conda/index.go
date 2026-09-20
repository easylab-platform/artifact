package conda

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/easylab-platform/artifact/core"
)

// GenerateRepodata renders a conda repodata.json from hosted packages in a
// subdir. conda's repodata is a plain JSON document:
//
//	{"info":{"subdir":"linux-64"},
//	 "packages":{"<name>-<ver>-<build>.tar.bz2":{"name","version","build",
//	   "build_number","size","md5","sha256","depends":[]}},
//	 "packages.conda":{...same for .conda archives...},
//	 "repodata_version":1}
//
// conda is UNSIGNED: clients verify package content against the md5/sha256
// recorded here, so a hosted channel needs no signing at all. (This is the
// one system-package ecosystem where hosted works with zero trust config.)
func GenerateRepodata(store artifactkit.HostedStore) artifactkit.Generator {
	return func(ctx context.Context, files []artifactkit.HostedFile) (map[string]artifactkit.GeneratedFile, error) {
		// Group by subdir (the first path segment of the hosted name).
		bySubdir := map[string]map[string]any{}
		condaBySubdir := map[string]map[string]any{}
		for _, f := range files {
			subdir, rel := splitSubdir(f.Name)
			if subdir == "" {
				continue
			}
			entry, ok := condaPackageEntry(ctx, store, f)
			if !ok {
				continue
			}
			name := baseName(rel)
			if strings.HasSuffix(f.Name, ".conda") {
				if condaBySubdir[subdir] == nil {
					condaBySubdir[subdir] = map[string]any{}
				}
				condaBySubdir[subdir][name] = entry
			} else {
				if bySubdir[subdir] == nil {
					bySubdir[subdir] = map[string]any{}
				}
				bySubdir[subdir][name] = entry
			}
		}
		out := map[string]artifactkit.GeneratedFile{}
		// conda always probes the channel's noarch subdir, so emit an (empty)
		// noarch/repodata.json even when only arch-specific packages exist.
		subdirs := unionSubdirs(bySubdir, condaBySubdir)
		subdirs["noarch"] = struct{}{}
		for subdir := range subdirs {
			doc := map[string]any{
				"info":             map[string]any{"subdir": subdir},
				"packages":         orEmpty(bySubdir[subdir]),
				"packages.conda":   orEmpty(condaBySubdir[subdir]),
				"repodata_version": 1,
			}
			body, err := json.MarshalIndent(doc, "", " ")
			if err != nil {
				return nil, err
			}
			out[subdir+"/repodata.json"] = artifactkit.GeneratedFile{Body: body, ContentType: "application/json"}
		}
		return out, nil
	}
}

// condaPackageEntry builds one repodata entry from a stored package.
func condaPackageEntry(ctx context.Context, store artifactkit.HostedStore, f artifactkit.HostedFile) (map[string]any, bool) {
	data, err := readBlob(ctx, store, f.Digest)
	if err != nil {
		return nil, false
	}
	name, version, build, bn := condaNVRB(baseName(f.Name))
	sum := sha256.Sum256(data)
	m5 := md5.Sum(data)
	md5sum := hex.EncodeToString(m5[:])
	return map[string]any{
		"name":         name,
		"version":      version,
		"build":        build,
		"build_number": bn,
		"size":         f.Size,
		"md5":          md5sum,
		"sha256":       hex.EncodeToString(sum[:]),
		"depends":      []string{},
		"timestamp":    time.Now().UTC().UnixMilli(),
		"subdir":       subdirOfName(f.Name),
	}, true
}

// condaNVRB derives (name, version, build, build_number) from a conda
// filename "<name>-<version>-<build>.conda|.tar.bz2".
func condaNVRB(filename string) (name, version, build string, buildNumber int) {
	stem := filename
	for _, suf := range []string{".conda", ".tar.bz2"} {
		stem = strings.TrimSuffix(stem, suf)
	}
	parts := strings.Split(stem, "-")
	if len(parts) < 3 {
		return stem, "0", "0", 0
	}
	build = parts[len(parts)-1]
	name = strings.Join(parts[:len(parts)-2], "-")
	version = parts[len(parts)-2]
	// A build string like "py310_0" carries the number after "_".
	if i := strings.LastIndexByte(build, '_'); i >= 0 {
		_, _ = fmt.Sscanf(build[i+1:], "%d", &buildNumber)
	}
	return name, version, build, buildNumber
}

// splitSubdir splits "linux-64/pkg.conda" into ("linux-64", "pkg.conda").
func splitSubdir(name string) (string, string) {
	i := strings.IndexByte(name, '/')
	if i < 0 {
		return "", name
	}
	return name[:i], name[i+1:]
}

func subdirOfName(name string) string {
	s, _ := splitSubdir(name)
	return s
}

func unionSubdirs(a, b map[string]map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

func readBlob(ctx context.Context, store artifactkit.HostedStore, digest string) ([]byte, error) {
	rd, err := store.Registry.Blobs.Open(ctx, digest)
	if err != nil || rd == nil {
		return nil, fmt.Errorf("blob %s unavailable", digest)
	}
	defer func() { _ = rd.Close() }()
	return io.ReadAll(rd)
}
