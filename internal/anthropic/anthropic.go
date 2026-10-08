// Package anthropic forwards the requests of Claude Code in the VM to the
// Claude API and adds the API key on the host, so that the VM never holds
// the key.
package anthropic

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
)

// Placeholder is the API key the VM gets in place of the real one. Claude
// Code wants a key to start. The forwarder throws it away.
const Placeholder = "sk-ant-api03-aibox-placeholder-the-key-stays-on-the-host"

// upstream is where the forwarder sends every request, whatever the
// request names.
const upstream = "https://api.anthropic.com"

// credentials are the headers the forwarder drops from a request before it
// adds the key, so that the VM can send no credential of its own.
var credentials = []string{"X-Api-Key", "Authorization", "Proxy-Authorization", "Cookie"}

// Forwarder returns the handler that sends each request to the Claude API
// with the key, and checks the API with roots. aibox can read no files
// once it is confined, so the roots are read before.
func Forwarder(key string, roots *x509.CertPool) http.Handler {
	target, _ := url.Parse(upstream)

	return newForwarder(key, target, &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true,
	})
}

func newForwarder(key string, target *url.URL, transport http.RoundTripper) http.Handler {
	forward := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)

			for _, name := range credentials {
				r.Out.Header.Del(name)
			}

			r.Out.Header.Set("X-Api-Key", key)
		},
		Transport: transport,
		// the answers of the API stream, and each event must reach Claude
		// Code at once
		FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Set-Cookie")

			return nil
		},
		// a failed request is a 502 for Claude Code that says why, not a
		// line on the terminal of the person
		ErrorLog: log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "aibox could not reach the Claude API: "+err.Error(), http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed(r) {
			http.Error(w, "aibox forwards only the Claude API", http.StatusForbidden)

			return
		}

		forward.ServeHTTP(w, r)
	})
}

// apiPath is a path of the API: plain letters, digits and separators.
// Escapes, backslashes and other characters are refused, since a server
// on the way could read them as other separators.
var apiPath = regexp.MustCompile(`^/v1/[A-Za-z0-9_.\-/]+$`)

// allowed tells whether the request goes to the API Claude Code uses: the
// endpoints under /v1/, and the check it makes before it starts. A path
// with dot segments or escapes is refused, since the API could resolve it
// to another endpoint.
func allowed(r *http.Request) bool {
	p := r.URL.Path
	if r.URL.RawPath != "" || path.Clean(p) != p {
		return false
	}

	return apiPath.MatchString(p) || p == "/api/hello"
}
