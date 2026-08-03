package hub

import "crypto/sha256"

const issuedResourceLimit = 16_384

// issuedResourceSet bounds capability memory even when an authorized backend
// returns a fresh, user-influenced URI on every call. Only digests are retained;
// an internal resource template handles reads without growing the public catalog.
type issuedResourceSet struct {
	entries [][sha256.Size]byte
	next    int
	present map[[sha256.Size]byte]struct{}
}

func (h *Hub) rememberResource(backendID, originalURI string) {
	if originalURI == "" {
		return
	}
	digest := sha256.Sum256([]byte(originalURI))
	h.issuedMu.Lock()
	resources := h.issuedResources[backendID]
	if resources == nil {
		resources = &issuedResourceSet{present: make(map[[sha256.Size]byte]struct{})}
		h.issuedResources[backendID] = resources
	}
	if _, exists := resources.present[digest]; exists {
		h.issuedMu.Unlock()
		return
	}
	if len(resources.entries) < issuedResourceLimit {
		resources.entries = append(resources.entries, digest)
	} else {
		delete(resources.present, resources.entries[resources.next])
		resources.entries[resources.next] = digest
		resources.next = (resources.next + 1) % issuedResourceLimit
	}
	resources.present[digest] = struct{}{}
	h.issuedMu.Unlock()
}

func (h *Hub) wasResourceIssued(backendID, originalURI string) bool {
	digest := sha256.Sum256([]byte(originalURI))
	h.issuedMu.RLock()
	resources := h.issuedResources[backendID]
	exists := false
	if resources != nil {
		_, exists = resources.present[digest]
	}
	h.issuedMu.RUnlock()
	return exists
}
