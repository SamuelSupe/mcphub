package hub

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/yosida95/uritemplate/v3"
)

var featureNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func exposeName(backendID, original string) (string, error) {
	if !featureNamePattern.MatchString(original) {
		return "", fmt.Errorf("invalid MCP feature name %q", original)
	}
	candidate := backendID + "." + original
	if len(candidate) <= 128 {
		return candidate, nil
	}
	sum := sha256.Sum256([]byte(original))
	suffix := hex.EncodeToString(sum[:6])
	prefix := backendID + "."
	keep := 128 - len(prefix) - len(suffix) - 1
	if keep < 1 {
		return "", fmt.Errorf("backend id %q leaves no room for feature name", backendID)
	}
	return prefix + original[:keep] + "." + suffix, nil
}

func backendFromName(name string) string {
	id, _, ok := strings.Cut(name, ".")
	if !ok {
		return ""
	}
	return id
}

func encodeResource(backendID, original string) string {
	return "mcphub://" + strings.ToLower(backendID) + "/r/" + base64.RawURLEncoding.EncodeToString([]byte(original))
}

func issuedResourceTemplate(backendID string) string {
	return "mcphub://" + strings.ToLower(backendID) + "/r/{resource}"
}

func decodeResource(exposed string) (backendID, original string, ok bool) {
	u, err := url.Parse(exposed)
	if err != nil || u.Scheme != "mcphub" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/r/") {
		return "", "", false
	}
	encoded := strings.TrimPrefix(u.Path, "/r/")
	if encoded == "" || strings.Contains(encoded, "/") {
		return "", "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return "", "", false
	}
	return u.Host, string(decoded), true
}

func backendFromURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mcphub" {
		return ""
	}
	return u.Host
}

type templateRoute struct {
	backendID      string
	original       string
	exposed        string
	originalParsed *uritemplate.Template
	exposedParsed  *uritemplate.Template
}

func newTemplateRoute(backendID, original string) (*templateRoute, error) {
	originalParsed, err := uritemplate.New(original)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(original))
	exposed := "mcphub://" + strings.ToLower(backendID) + "/t/" + hex.EncodeToString(sum[:])
	if variables := originalParsed.Varnames(); len(variables) > 0 {
		exposed += "{?" + strings.Join(variables, ",") + "}"
	}
	exposedParsed, err := uritemplate.New(exposed)
	if err != nil {
		return nil, err
	}
	return &templateRoute{
		backendID:      backendID,
		original:       original,
		exposed:        exposed,
		originalParsed: originalParsed,
		exposedParsed:  exposedParsed,
	}, nil
}

func (r *templateRoute) expand(exposedURI string) (string, error) {
	values := r.exposedParsed.Match(exposedURI)
	if values == nil {
		return "", fmt.Errorf("URI does not match template %q", r.exposed)
	}
	return r.originalParsed.Expand(values)
}
