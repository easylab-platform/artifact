// Package maven implements the Maven 2 repository protocol (group/artifact/
// version/filename layout, hash sidecars, maven-metadata.xml, PUT/HEAD/DELETE,
// pull-through from Maven Central). Mirror of the pkglab reference.
package maven

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	return s, nil
}

func init() {
	artifactkit.Register("maven", NewHandler)
	artifactkit.RegisterNamespace("maven", artifactkit.MavenNamespace)
}

type coords struct {
	artifactID string
	version    string
}

func parseMavenPath(p string) (coords, bool) {
	parts := splitPath(strings.Trim(p, "/"))
	if len(parts) < 3 {
		return coords{}, false
	}
	return coords{
		artifactID: parts[len(parts)-3],
		version:    parts[len(parts)-2],
	}, true
}

func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/maven")
	path = strings.TrimPrefix(path, "/maven")
	path = strings.Trim(path, "/")

	if path == "archetype-catalog.xml" {
		artifactkit.Text(w, http.StatusOK, `<?xml version="1.0" encoding="UTF-8"?><archetype-catalog></archetype-catalog>`, "application/xml")
		return
	}

	switch {
	case strings.HasSuffix(path, ".md5") || strings.HasSuffix(path, ".sha1"):
		if r.Method == http.MethodPut {
			c, _ := parseMavenPath(strings.TrimSuffix(strings.TrimSuffix(path, ".md5"), ".sha1"))
			s.putPath(w, r, path, c)
			return
		}
		s.hashFile(w, r, path)
	case strings.HasSuffix(path, "maven-metadata.xml"):
		if r.Method == http.MethodPut {
			c, ok := parseMavenPath(path)
			if !ok {
				artifactkit.Error(w, http.StatusNotFound, "invalid path")
				return
			}
			s.putPath(w, r, path, coords{c.artifactID, "maven-metadata"})
			return
		}
		s.metadataXML(w, r, path)
	default:
		c, ok := parseMavenPath(path)
		if !ok {
			artifactkit.Error(w, http.StatusNotFound, "invalid maven path")
			return
		}
		switch r.Method {
		case http.MethodPut:
			s.putPath(w, r, path, c)
		case http.MethodHead:
			s.headPath(w, r, path, c)
		case http.MethodGet:
			s.getPath(w, r, path, c)
		case http.MethodDelete:
			if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "maven") {
				return
			}
			s.deletePath(w, r, path, c)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (s *State) getPath(w http.ResponseWriter, r *http.Request, p string, c coords) {
	filename := pathLast(p)
	if art, err := s.Registry.Meta.Get(r.Context(), "maven", c.artifactID, c.version); err == nil {
		for _, b := range art.Blobs {
			if b.Name == filename {
				if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), b.Digest, "application/octet-stream", filename) {
					return
				}
				continue
			}
		}
	}
	// Stream into the CAS (no full buffering of a large jar). FetchToBlob
	// follows redirects, covering JitPack's 302 and the Gradle Plugin Portal's
	// 303 to Maven Central.
	digest, size, ok := s.Registry.FetchToBlob(r.Context(), "maven", "/"+p)
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	storeVersionBlob(s.Registry, c.artifactID, c.version, filename, digest, size, "pull", r.Context())
	if artifactkit.ServeBlobAtNamed(w, r, s.Registry.Blobs, r.Context(), digest, "application/octet-stream", filename) {
		return
	}
	artifactkit.Error(w, http.StatusBadGateway, "cache error")
}

func (s *State) headPath(w http.ResponseWriter, r *http.Request, p string, c coords) {
	filename := pathLast(p)
	if art, err := s.Registry.Meta.Get(r.Context(), "maven", c.artifactID, c.version); err == nil {
		for _, b := range art.Blobs {
			if b.Name == filename {
				w.Header().Set("Content-Length", itoa(b.Size))
				w.WriteHeader(http.StatusOK)
				return
			}
		}
	}
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusOK)
}

func (s *State) putPath(w http.ResponseWriter, r *http.Request, p string, c coords) {
	if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "maven") {
		return
	}
	filename := pathLast(p)
	artifactkit.LimitBody(w, r)
	// The filename and coordinates come from the URL, so the body can stream
	// straight into the CAS without buffering a (potentially large) jar.
	storeVersionStream(s.Registry, c.artifactID, c.version, filename, r.Body, "push", r.Context())
	artifactkit.JSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *State) deletePath(w http.ResponseWriter, r *http.Request, p string, c coords) {
	filename := pathLast(p)
	if art, err := s.Registry.Meta.Get(r.Context(), "maven", c.artifactID, c.version); err == nil {
		var kept, removed []artifactkit.Descriptor
		for _, b := range art.Blobs {
			if b.Name == filename {
				removed = append(removed, b)
			} else {
				kept = append(kept, b)
			}
		}
		art.Blobs = kept
		if len(kept) == 0 {
			artifactkit.LogMetaErr("meta delete", s.Registry.Meta.Delete(r.Context(), "maven", c.artifactID, c.version))
		} else {
			artifactkit.LogMetaErr("meta put", s.Registry.Meta.Put(r.Context(), art))
		}
		for _, b := range removed {
			artifactkit.LogMetaErr("blob delete", s.Registry.Blobs.Delete(r.Context(), b.Digest))
		}
	}
	artifactkit.JSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *State) hashFile(w http.ResponseWriter, r *http.Request, p string) {
	isMD5 := strings.HasSuffix(p, ".md5")
	orig := strings.TrimSuffix(strings.TrimSuffix(p, ".md5"), ".sha1")
	c, ok := parseMavenPath(orig)
	if !ok {
		artifactkit.Error(w, http.StatusNotFound, "not found")
		return
	}
	filename := pathLast(orig)
	if art, err := s.Registry.Meta.Get(r.Context(), "maven", c.artifactID, c.version); err == nil {
		for _, b := range art.Blobs {
			if b.Name == filename {
				h, err := s.Registry.Blobs.HashesFor(r.Context(), b.Digest)
				if err == nil {
					val := h.SHA1
					if isMD5 {
						val = h.MD5
					}
					if val != "" {
						artifactkit.Text(w, http.StatusOK, val, "text/plain")
						return
					}
				}
			}
		}
	}
	fetched, err := s.Registry.Fetch(r.Context(), "maven", "", "/"+p)
	if err != nil {
		// JitPack 302s to the built version and the Gradle Plugin Portal 303s
		// to Maven Central; both are content we want to mirror, so follow the
		// redirect and cache the final body.
		fetched, err = s.Registry.FetchPathFollow(r.Context(), "maven", "/"+p)
		if err != nil {
			artifactkit.Error(w, http.StatusNotFound, "not found")
			return
		}
	}
	artifactkit.Text(w, http.StatusOK, string(fetched.Data), "text/plain")
}

func (s *State) metadataXML(w http.ResponseWriter, r *http.Request, p string) {
	clean := strings.Trim(strings.TrimSuffix(p, "maven-metadata.xml"), "/")
	parts := splitPath(clean)
	if len(parts) == 0 || parts[0] == "" {
		artifactkit.Text(w, http.StatusOK, "<metadata></metadata>", "application/xml")
		return
	}
	if len(parts) >= 3 && strings.HasSuffix(parts[len(parts)-1], "-SNAPSHOT") {
		s.versionMetadataXML(w, r, parts)
		return
	}
	artifactID := parts[len(parts)-1]
	groupID := strings.Join(parts[:len(parts)-1], ".")
	versions, _ := s.Registry.Meta.ListVersions(r.Context(), "maven", artifactID)
	artifactkit.SortSemver(versions)
	if len(versions) == 0 {
		// No local versions: pull the upstream metadata through so Maven can
		// resolve plugins/dependencies against this mirror (Maven reads
		// maven-metadata.xml to enumerate versions before downloading).
		if body, err := s.fetchUpstreamMetadata(r, clean); err == nil {
			artifactkit.Text(w, http.StatusOK, body, "application/xml")
			return
		}
		artifactkit.Text(w, http.StatusOK, `<?xml version="1.0" encoding="UTF-8"?><metadata><groupId>`+groupID+`</groupId><artifactId>`+artifactID+`</artifactId><versioning><versions></versions></versioning></metadata>`, "application/xml")
		return
	}
	latest := artifactkit.HighestVersion(versions)
	if latest == "" && len(versions) > 0 {
		latest = versions[len(versions)-1]
	}
	// release = highest non-SNAPSHOT version (versions are ascending).
	release := latest
	for _, v := range versions {
		if !strings.Contains(v, "-SNAPSHOT") {
			release = v
		}
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><metadata><groupId>` + groupID + `</groupId><artifactId>` + artifactID + `</artifactId><versioning><latest>` + latest + `</latest><release>` + release + `</release><versions>`)
	for _, v := range versions {
		sb.WriteString(`<version>` + v + `</version>`)
	}
	sb.WriteString(`</versions><lastUpdated>20240101120000</lastUpdated></versioning></metadata>`)
	artifactkit.Text(w, http.StatusOK, sb.String(), "application/xml")
}

// fetchUpstreamMetadata pulls a maven-metadata.xml through from the upstream
// (used when this mirror has no local versions for the GA, so Maven can still
// enumerate versions — e.g. that of a plugin it needs to resolve).
func (s *State) fetchUpstreamMetadata(r *http.Request, rel string) (string, error) {
	remote, err := s.Registry.RemoteCtx(r.Context(), "maven")
	if err != nil {
		return "", errNoUpstream
	}
	body, err := remote.GetBytes(r.Context(), "/"+strings.Trim(rel, "/")+"/maven-metadata.xml")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// errNoUpstream signals an air-gapped (no upstream) mirror.
var errNoUpstream = errNoUpstreamType("no upstream")

type errNoUpstreamType string

func (e errNoUpstreamType) Error() string { return string(e) }

func (s *State) versionMetadataXML(w http.ResponseWriter, r *http.Request, parts []string) {
	version := parts[len(parts)-1]
	artifactID := parts[len(parts)-2]
	groupID := strings.Join(parts[:len(parts)-2], ".")
	ts, build := "20240101.120000", "1"
	if art, err := s.Registry.Meta.Get(r.Context(), "maven", artifactID, version); err == nil {
		for _, b := range art.Blobs {
			if strings.Contains(b.Name, "-"+version) {
				if v := snapshotTimestampFromName(b.Name); v != "" {
					ts = v
				}
			}
		}
	}
	timestamped := strings.Replace(version, "-SNAPSHOT", "-"+ts+"-"+build, 1)
	sb := `<?xml version="1.0" encoding="UTF-8"?><metadata><groupId>` + groupID + `</groupId><artifactId>` + artifactID + `</artifactId><version>` + version + `</version><versioning><snapshot><timestamp>` + ts + `</timestamp><buildNumber>` + build + `</buildNumber></snapshot><lastUpdated>20240101120000</lastUpdated><snapshotVersions><snapshotVersion><extension>jar</extension><value>` + timestamped + `</value><updated>20240101120000</updated></snapshotVersion><snapshotVersion><extension>pom</extension><value>` + timestamped + `</value><updated>20240101120000</updated></snapshotVersion></snapshotVersions></versioning></metadata>`
	artifactkit.Text(w, http.StatusOK, sb, "application/xml")
}

func snapshotTimestampFromName(name string) string {
	b := []byte(name)
	for i := 0; i+16 <= len(b); i++ {
		if !allDigits(b[i : i+8]) {
			continue
		}
		j := i + 8
		if j >= len(b) || b[j] != '.' {
			continue
		}
		if j+7 > len(b) || !allDigits(b[j+1:j+7]) {
			continue
		}
		j += 7
		if j >= len(b) || b[j] != '-' {
			continue
		}
		j++
		k := j
		for k < len(b) && b[k] >= '0' && b[k] <= '9' {
			k++
		}
		if k == j {
			continue
		}
		return string(b[i:j]) + string(b[j:k])
	}
	return ""
}

func allDigits(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(b) > 0
}

// storeVersionBlob records a blob already streamed into the CAS (digest/size
// known, bytes not in memory) under (artifactID, version, filename).
func storeVersionBlob(reg *artifactkit.Registry, artifactID, version, filename, digest string, size int64, source string, ctx context.Context) {
	if version == "" {
		version = "0.0.0"
	}
	art, _ := reg.Meta.Get(ctx, "maven", artifactID, version)
	art.Format = "maven"
	art.Source = source
	art.Repository = artifactID
	art.Version = version
	var removed, kept []artifactkit.Descriptor
	for _, b := range art.Blobs {
		if b.Name == filename {
			removed = append(removed, b)
		} else {
			kept = append(kept, b)
		}
	}
	art.Blobs = append(kept, artifactkit.Descriptor{Digest: digest, Size: size, Name: filename})
	for _, b := range removed {
		if b.Digest != digest {
			artifactkit.LogMetaErr("blob delete", reg.Blobs.Delete(ctx, b.Digest))
		}
	}
	artifactkit.LogMetaErr("meta put", reg.Meta.Put(ctx, art))
}

// storeVersionStream streams the body into the CAS (no full in-memory copy) and
// hashes it in the same pass, then indexes the (find-replace by filename) blob.
func storeVersionStream(reg *artifactkit.Registry, artifactID, version, filename string, body io.Reader, source string, ctx context.Context) {
	if version == "" {
		version = "0.0.0"
	}
	art, _ := reg.Meta.Get(ctx, "maven", artifactID, version)
	art.Format = "maven"
	art.Source = source
	art.Repository = artifactID
	art.Version = version
	stored, err := reg.StoreStream(ctx, body)
	if err != nil || stored.Size == 0 {
		// An empty body stores no blob, matching the byte-slice path.
		artifactkit.LogMetaErr("store stream", err)
		artifactkit.LogMetaErr("meta put", reg.Meta.Put(ctx, art))
		return
	}
	var removed, kept []artifactkit.Descriptor
	for _, b := range art.Blobs {
		if b.Name == filename {
			removed = append(removed, b)
		} else {
			kept = append(kept, b)
		}
	}
	art.Blobs = kept
	art.Blobs = append(art.Blobs, artifactkit.Descriptor{Digest: stored.Digest, Size: stored.Size, Name: filename})
	for _, b := range removed {
		if b.Digest != stored.Digest {
			artifactkit.LogMetaErr("blob delete", reg.Blobs.Delete(ctx, b.Digest))
		}
	}
	artifactkit.LogMetaErr("meta put", reg.Meta.Put(ctx, art))
}

func splitPath(p string) []string {
	if p == "" {
		return []string{}
	}
	return strings.Split(strings.Trim(p, "/"), "/")
}

func pathLast(p string) string {
	parts := splitPath(p)
	if len(parts) == 0 {
		return p
	}
	return parts[len(parts)-1]
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
