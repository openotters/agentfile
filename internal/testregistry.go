package internal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type Registry struct {
	Server *httptest.Server

	mu        sync.RWMutex
	blobs     map[string][]byte
	manifests map[string][]byte
	uploads   map[string][]byte
	uploadSeq int
}

func New() *Registry {
	r := &Registry{
		blobs:     make(map[string][]byte),
		manifests: make(map[string][]byte),
		uploads:   make(map[string][]byte),
	}
	r.Server = httptest.NewServer(r)

	return r
}

func (r *Registry) Host() string {
	return strings.TrimPrefix(r.Server.URL, "http://")
}

func (r *Registry) Close() {
	r.Server.Close()
}

func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Path

	switch {
	case path == "/v2/" || path == "/v2":
		w.WriteHeader(http.StatusOK)

	case req.Method == http.MethodHead && strings.Contains(path, "/manifests/"):
		r.headManifest(w, req)

	case req.Method == http.MethodGet && strings.Contains(path, "/manifests/"):
		r.getManifest(w, req)

	case req.Method == http.MethodPut && strings.Contains(path, "/manifests/"):
		r.putManifest(w, req)

	case req.Method == http.MethodGet && strings.Contains(path, "/blobs/"):
		r.getBlob(w, req)

	case req.Method == http.MethodHead && strings.Contains(path, "/blobs/") && !strings.Contains(path, "/uploads/"):
		r.headBlob(w, req)

	case req.Method == http.MethodPost && strings.Contains(path, "/blobs/uploads"):
		r.startUpload(w, req)

	case (req.Method == http.MethodPut || req.Method == http.MethodPatch) && strings.Contains(path, "/blobs/uploads/"):
		r.completeUpload(w, req)

	default:
		http.NotFound(w, req)
	}
}

func (r *Registry) headManifest(w http.ResponseWriter, req *http.Request) {
	ref := lastSegment(req.URL.Path)

	r.mu.RLock()
	data, ok := r.manifests[ref]
	if !ok {
		for digest, d := range r.manifests {
			if strings.HasPrefix(digest, "sha256:") && digest == ref {
				data, ok = d, true

				break
			}
		}
	}
	r.mu.RUnlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	digest := "sha256:" + sha256Hex(data)
	w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
}

func (r *Registry) getManifest(w http.ResponseWriter, req *http.Request) {
	ref := lastSegment(req.URL.Path)

	r.mu.RLock()
	data, ok := r.manifests[ref]
	if !ok {
		for digest, d := range r.manifests {
			if digest == ref {
				data, ok = d, true

				break
			}
		}
	}
	r.mu.RUnlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	digest := "sha256:" + sha256Hex(data)
	w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	w.Header().Set("Docker-Content-Digest", digest)
	_, _ = w.Write(data)
}

func (r *Registry) putManifest(w http.ResponseWriter, req *http.Request) {
	ref := lastSegment(req.URL.Path)

	data, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	digest := "sha256:" + sha256Hex(data)

	r.mu.Lock()
	r.manifests[ref] = data
	r.manifests[digest] = data
	r.mu.Unlock()

	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

func (r *Registry) headBlob(w http.ResponseWriter, req *http.Request) {
	digest := lastSegment(req.URL.Path)

	r.mu.RLock()
	data, ok := r.blobs[digest]
	r.mu.RUnlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusOK)
}

func (r *Registry) getBlob(w http.ResponseWriter, req *http.Request) {
	digest := lastSegment(req.URL.Path)

	r.mu.RLock()
	data, ok := r.blobs[digest]
	r.mu.RUnlock()

	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	w.Header().Set("Docker-Content-Digest", digest)
	_, _ = w.Write(data)
}

func (r *Registry) startUpload(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.uploadSeq++
	id := fmt.Sprintf("%d", r.uploadSeq)
	r.uploads[id] = nil
	r.mu.Unlock()

	repo := extractRepo(req.URL.Path)
	w.Header().Set("Location", fmt.Sprintf("/v2/%s/blobs/uploads/%s", repo, id))
	w.WriteHeader(http.StatusAccepted)
}

func (r *Registry) completeUpload(w http.ResponseWriter, req *http.Request) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	digest := req.URL.Query().Get("digest")
	if digest == "" {
		digest = "sha256:" + sha256Hex(data)
	}

	r.mu.Lock()
	r.blobs[digest] = data
	r.mu.Unlock()

	w.Header().Set("Docker-Content-Digest", digest)
	w.WriteHeader(http.StatusCreated)
}

func lastSegment(path string) string {
	i := strings.LastIndex(path, "/")
	if i == -1 {
		return path
	}

	return path[i+1:]
}

func extractRepo(path string) string {
	path = strings.TrimPrefix(path, "/v2/")
	if i := strings.Index(path, "/blobs/"); i != -1 {
		return path[:i]
	}
	if i := strings.Index(path, "/manifests/"); i != -1 {
		return path[:i]
	}

	return path
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
