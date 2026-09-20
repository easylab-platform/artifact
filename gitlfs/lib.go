// Package gitlfs implements the Git LFS batch API with a CAS-backed object
// store: /info/lfs/objects/batch negotiates uploads/downloads and objects
// are content-addressed (oid = sha256, exactly our CAS digest), so an LFS
// object and an easyvcs blob share storage when equal.
//
// Wire format (LFS basic transfer adapter):
//
//	POST /info/lfs/objects/batch
//	  {"operation":"upload"|"download","objects":[{"oid":"<sha256>","size":N}]}
//	  → 200 {"objects":[{"oid","size","authenticated":true,
//	       "actions":{"upload"|"download":{"href","header"}}}]}
//	PUT <upload href>        object bytes
//	GET <download href>      object bytes
package gitlfs

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/easylab-platform/artifact/core"
)

type State struct {
	Registry *artifactkit.Registry
	Auth     artifactkit.Auth
	// SelfBase is the external base URL prepended to action hrefs. It must be
	// the address the CLIENT uses to reach easylab (not the request Host,
	// which is the upstream name when the request arrived through the egress
	// proxy's rewrite relay). Empty falls back to deriving from the request.
	SelfBase string
}

func NewHandler(reg *artifactkit.Registry, cfg map[string]any) (http.Handler, error) {
	s := &State{Registry: reg}
	if a, ok := cfg["auth"].(artifactkit.Auth); ok {
		s.Auth = a
	}
	if v, ok := cfg["self_base"].(string); ok {
		s.SelfBase = strings.TrimSuffix(v, "/")
	}
	return s, nil
}

func init() { artifactkit.Register("gitlfs", NewHandler) }

// batchRequest is the LFS batch API request body.
type batchRequest struct {
	Operation string       `json:"operation"` // "upload" | "download"
	Objects   []batchObjIn `json:"objects"`
}

type batchObjIn struct {
	Oid  string `json:"oid"`
	Size int64  `json:"size"`
}

type batchResponse struct {
	Objects []batchObjOut `json:"objects"`
}

type batchObjOut struct {
	Oid           string          `json:"oid"`
	Size          int64           `json:"size"`
	Authenticated bool            `json:"authenticated"`
	Actions       map[string]href `json:"actions,omitempty"`
}

type href struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header,omitempty"`
}

// ServeHTTP dispatches POST /artifacts/gitlfs/{repo}/info/lfs/objects/batch and
// GET/PUT /artifacts/gitlfs/{repo}/objects/{oid}.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/artifacts/gitlfs/")
	path = strings.TrimPrefix(path, "gitlfs/")
	path = strings.Trim(path, "/")
	switch {
	case strings.HasSuffix(path, "/info/lfs/objects/batch") && r.Method == http.MethodPost:
		repo := strings.TrimSuffix(path, "/info/lfs/objects/batch")
		if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "gitlfs") && r.URL.Query().Get("op") != "download" {
			return
		}
		s.batch(w, r, repo)
	case strings.Contains(path, "/objects/") && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		repo := strings.SplitN(path, "/objects/", 2)[0]
		oid := path[strings.Index(path, "/objects/")+len("/objects/"):]
		s.serveObject(w, r, repo, oid)
	default:
		artifactkit.Error(w, http.StatusNotFound, "not found")
	}
}

// batch answers the negotiation. Download actions are always offered (the
// object href serves from the CAS). Upload actions are offered for write
// callers; the actual PUT stores the bytes.
func (s *State) batch(w http.ResponseWriter, r *http.Request, repo string) {
	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		artifactkit.Error(w, http.StatusBadRequest, "bad batch body")
		return
	}
	base := s.baseFor(r)
	out := batchResponse{Objects: make([]batchObjOut, 0, len(req.Objects))}
	for _, o := range req.Objects {
		entry := batchObjOut{Oid: o.Oid, Size: o.Size, Authenticated: true}
		switch req.Operation {
		case "download":
			entry.Actions = map[string]href{"download": {Href: base + "/artifacts/gitlfs/" + repo + "/objects/" + o.Oid}}
		case "upload":
			entry.Actions = map[string]href{"upload": {Href: base + "/artifacts/gitlfs/" + repo + "/objects/" + o.Oid}}
		}
		out.Objects = append(out.Objects, entry)
	}
	artifactkit.JSON(w, http.StatusOK, out)
}

// serveObject GETs (from the CAS) or PUTs (into the CAS) one object.
func (s *State) serveObject(w http.ResponseWriter, r *http.Request, repo, oid string) {
	if !validOID(oid) {
		artifactkit.Error(w, http.StatusBadRequest, "invalid oid (want sha256 hex)")
		return
	}
	digest := "sha256:" + oid
	switch r.Method {
	case http.MethodGet:
		if !artifactkit.ServeBlobAt(w, r, s.Registry.Blobs, r.Context(), digest, "application/octet-stream") {
			artifactkit.Error(w, http.StatusNotFound, "object not found")
		}
	case http.MethodPut:
		if !artifactkit.AuthorizeWriteScoped(w, r, s.Auth, s.Registry, "gitlfs") {
			return
		}
		if _, _, err := s.Registry.Blobs.Put(r.Context(), r.Body, digest); err != nil {
			artifactkit.Error(w, http.StatusBadRequest, "digest mismatch: "+err.Error())
			return
		}
		artifactkit.LogMetaErr("gitlfs index", s.Registry.Meta.Put(r.Context(), artifactkit.Artifact{
			Format: "gitlfs", Repository: repo, Version: oid,
			MediaType: "application/octet-stream", Digest: digest,
			Blobs:  []artifactkit.Descriptor{{Digest: digest, Name: oid}},
			Source: "push",
		}))
		w.WriteHeader(http.StatusOK)
	}
}

// validOID reports whether oid is 64 lowercase hex chars.
func validOID(oid string) bool {
	if len(oid) != 64 {
		return false
	}
	for _, c := range oid {
		isHex := ('0' <= c && c <= '9') || ('a' <= c && c <= 'f')
		if !isHex {
			return false
		}
	}
	return true
}

// baseFor returns the external base for action hrefs: the configured
// SelfBase when set (the client-reachable address), else derived from the
// request. Requests that arrived through the egress proxy carry the upstream
// Host, so SelfBase is required in that deployment.
func (s *State) baseFor(r *http.Request) string {
	if s.SelfBase != "" {
		return s.SelfBase
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
