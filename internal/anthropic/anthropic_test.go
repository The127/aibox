package anthropic

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// forwarderTo returns a server with the forwarder in front of the handler.
func forwarderTo(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	target, err := url.Parse(api.URL)
	require.NoError(t, err)

	forwarder := httptest.NewServer(newForwarder("the-real-key", target, http.DefaultTransport))
	t.Cleanup(forwarder.Close)

	return forwarder
}

func TestForwarderPutsTheKeyInPlaceOfTheCredentialsOfTheVM(t *testing.T) {
	// arrange
	got := make(chan http.Header, 1)
	forwarder := forwarderTo(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()

		w.Header().Set("Set-Cookie", "session=x")
		_, _ = io.WriteString(w, "ok")
	})

	request, err := http.NewRequest(http.MethodPost, forwarder.URL+"/v1/messages?beta=true", strings.NewReader("{}"))
	require.NoError(t, err)
	request.Header.Set("X-Api-Key", Placeholder)
	request.Header.Set("Authorization", "Bearer other")
	request.Header.Set("Cookie", "a=b")
	request.Header.Set("Anthropic-Beta", "some-beta")

	// act
	response, err := http.DefaultClient.Do(request)

	// assert
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Empty(t, response.Header.Values("Set-Cookie"))

	headers := <-got
	assert.Equal(t, []string{"the-real-key"}, headers.Values("X-Api-Key"))
	assert.Empty(t, headers.Values("Authorization"))
	assert.Empty(t, headers.Values("Cookie"))
	assert.Equal(t, "some-beta", headers.Get("Anthropic-Beta"))
}

func TestForwarderRefusesWhatIsNoClaudeAPI(t *testing.T) {
	// arrange
	called := false
	forwarder := forwarderTo(t, func(http.ResponseWriter, *http.Request) { called = true })

	for _, path := range []string{"/", "/api/oauth/token", "/v1", "/../v1/messages", "/v1/../api/oauth/token", "/v1/%2e%2e/api/oauth/token", "/v1/messages/../../api", "/v1//messages", "/v1/a%2fb", "/v1/%41", "/v1/a\\..\\..\\api"} {
		// act
		response, err := http.Get(forwarder.URL + path) //nolint:noctx // a test against a local server

		// assert
		require.NoError(t, err)
		_ = response.Body.Close()
		assert.Equal(t, http.StatusForbidden, response.StatusCode, path)
	}

	assert.False(t, called)
}

func TestForwarderSaysWhyItCouldNotReachTheAPI(t *testing.T) {
	// arrange
	target, err := url.Parse("http://127.0.0.1:1")
	require.NoError(t, err)

	forwarder := httptest.NewServer(newForwarder("the-real-key", target, http.DefaultTransport))
	t.Cleanup(forwarder.Close)

	// act
	response, err := http.Post(forwarder.URL+"/v1/messages", "application/json", strings.NewReader("{}")) //nolint:noctx // a test against a local server

	// assert
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadGateway, response.StatusCode)
	assert.Contains(t, string(body), "aibox could not reach the Claude API")
	assert.NotContains(t, string(body), "the-real-key")
}

func TestForwarderTakesTheCheckClaudeCodeMakesBeforeItStarts(t *testing.T) {
	// arrange
	forwarder := forwarderTo(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	request, err := http.NewRequest(http.MethodHead, forwarder.URL+"/api/hello", nil)
	require.NoError(t, err)

	// act
	response, err := http.DefaultClient.Do(request)

	// assert
	require.NoError(t, err)
	_ = response.Body.Close()
	assert.Equal(t, http.StatusOK, response.StatusCode)
}

func TestForwarderStreamsEachEventAtOnce(t *testing.T) {
	// arrange
	release := make(chan struct{})
	forwarder := forwarderTo(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: one\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "event: two\n\n")
	})
	defer close(release)

	// act
	response, err := http.Post(forwarder.URL+"/v1/messages", "application/json", strings.NewReader("{}")) //nolint:noctx // a test against a local server

	// assert
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()

	line := make(chan string, 1)
	go func() {
		text, _ := bufio.NewReader(response.Body).ReadString('\n')
		line <- text
	}()

	select {
	case text := <-line:
		assert.Equal(t, "event: one\n", text)
	case <-time.After(5 * time.Second):
		t.Fatal("the first event did not come before the answer ended")
	}
}

func TestForwarderSendsEveryRequestToTheAPI(t *testing.T) {
	// arrange
	hosts := make(chan string, 1)
	forwarder := forwarderTo(t, func(_ http.ResponseWriter, r *http.Request) { hosts <- r.Host })

	request, err := http.NewRequest(http.MethodGet, forwarder.URL+"/v1/models", nil)
	require.NoError(t, err)
	request.Host = "evil.example"

	// act
	response, err := http.DefaultClient.Do(request)

	// assert
	require.NoError(t, err)
	_ = response.Body.Close()
	assert.NotEqual(t, "evil.example", <-hosts)
}
