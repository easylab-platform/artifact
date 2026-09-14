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
	"io"
	"net/http"
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

// ServeHTTP dispatches POST /pkgs/gitlfs/{repo}/info/lfs/objects/batch and
// GET/PUT /pkgs/gitlfs/{repo}/objects/{oid}.
func (s *State) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/pkgs/gitlfs/")
	path = strings.TrimPrefix(path, "gitlfs/")
	path = strings.Trim(path, "/")
	switch {
	case strings.HasSuffix(path, "/info/lfs/objects/batch") && r.Method == http.MethodPost:
		repo := strings.TrimSuffix(path, "/info/lfs/objects/batch")
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) && r.URL.Query().Get("op") != "download" {
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
	base := selfBase(r)
	out := batchResponse{Objects: make([]batchObjOut, 0, len(req.Objects))}
	for _, o := range req.Objects {
		entry := batchObjOut{Oid: o.Oid, Size: o.Size, Authenticated: true}
		switch req.Operation {
		case "download":
			entry.Actions = map[string]href{"download": {Href: base + "/pkgs/gitlfs/" + repo + "/objects/" + o.Oid}}
		case "upload":
			entry.Actions = map[string]href{"upload": {Href: base + "/pkgs/gitlfs/" + repo + "/objects/" + o.Oid}}
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
		rd, err := s.Registry.Blobs.Open(r.Context(), digest)
		if err != nil || rd == nil {
			artifactkit.Error(w, http.StatusNotFound, "object not found")
			return
		}
		defer func() { _ = rd.Close() }()
		artifactkit.OctetResponse(w, streamAll(rd))
	case http.MethodPut:
		if !artifactkit.AuthorizeWrite(w, r, s.Auth) {
			return
		}
		if _, err := s.Registry.Blobs.PutIfAbsent(r.Context(), digest, r.Body); err != nil {
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

// selfBase derives the external base for action hrefs from the request.
func selfBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// streamAll reads a reader into memory (LFS objects are individually capped
// by the CAS; streaming straight through would need http.ServeContent which
// needs a Seeker — rd IS a ReadSeekCloser so we could, but plain read keeps
// the code simple and sizes bounded by the caller's limits).
func streamAll(rd io.Reader) []byte {
	data, _ := io.ReadAll(rd)
	return data
}
