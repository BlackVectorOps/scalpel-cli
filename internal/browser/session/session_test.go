package session_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/xkilldash9x/scalpel-cli/api/schemas"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/jsexec"
	"github.com/xkilldash9x/scalpel-cli/internal/browser/session"
	"github.com/xkilldash9x/scalpel-cli/internal/config"
)

// Helper to create a new test session
func setupTestSession(t *testing.T) (*session.Session, *config.Config, chan schemas.Finding) {
	cfg := config.NewDefaultConfig()
	// Disable delays for faster testing
	cfg.Browser.Humanoid.Enabled = false
	cfg.Network.PostLoadWait = 10 * time.Millisecond // Short stabilization time
    cfg.Network.CaptureResponseBodies = true // Enable body capture for HAR tests

	logger := zap.NewNop()
	jsRuntime := jsexec.NewRuntime()
	findingsChan := make(chan schemas.Finding, 10)

	s, err := session.NewSession(context.Background(), cfg, schemas.DefaultPersona, logger, jsRuntime, findingsChan)
	require.NoError(t, err)

	// Initialize is required after NewSession
	err = s.Initialize(context.Background(), nil, "", "")
	require.NoError(t, err)

	return s, cfg, findingsChan
}

// -- Lifecycle and State Tests --

func TestSession_Lifecycle(t *testing.T) {
	s, _, _ := setupTestSession(t)

	assert.NotEmpty(t, s.ID())
	ctx := s.GetContext()
	assert.NoError(t, ctx.Err(), "Context should not be cancelled initially")

	closed := false
	s.SetOnClose(func() {
		closed = true
	})

	err := s.Close(context.Background())
	require.NoError(t, err)

	assert.True(t, closed, "onClose callback should be executed")
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "Context should be cancelled after close")
}

func TestSession_NavigationAndStateUpdate(t *testing.T) {
	// 1. Setup Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			// Verify User-Agent is set by the session
			assert.Equal(t, schemas.DefaultPersona.UserAgent, r.Header.Get("User-Agent"))
			fmt.Fprintln(w, `<html><head><title>Start Page</title></head><body><h1>Welcome</h1></body></html>`)
		}
	}))
	defer server.Close()

	// 2. Setup Session
	s, _, _ := setupTestSession(t)
	defer s.Close(context.Background())

	// 3. Navigate
	targetURL := server.URL + "/start"
	err := s.Navigate(context.Background(), targetURL)
	require.NoError(t, err)

	// 4. Verify State
	assert.Equal(t, targetURL, s.GetCurrentURL())

	// Verify DOM snapshot
	snapshot, err := s.GetDOMSnapshot(context.Background())
	require.NoError(t, err)
	content, _ := io.ReadAll(snapshot)
	domContent := string(content)

	assert.Contains(t, domContent, "<title>Start Page</title>")
	assert.Contains(t, domContent, "<h1>Welcome</h1>")
}

func TestSession_HandleRedirect(t *testing.T) {
	// 1. Setup Mock Server for redirection
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			fmt.Fprintln(w, `<html></html>`)
		} else if r.URL.Path == "/redirect" {
			// Ensure the Referer header is set correctly during the redirect chain
			assert.Contains(t, r.Header.Get("Referer"), "/start")
			http.Redirect(w, r, "/final", http.StatusFound) // 302 Redirect
		} else if r.URL.Path == "/final" {
			assert.Contains(t, r.Header.Get("Referer"), "/redirect")
			fmt.Fprintln(w, `<html><title>Final Page</title></html>`)
		}
	}))
	defer server.Close()

	// 2. Setup Session
	s, _, _ := setupTestSession(t)
	defer s.Close(context.Background())

	// Initial navigation to set a base URL/Referer
	s.Navigate(context.Background(), server.URL+"/start")

	// 3. Navigate to the redirect URL
	err := s.Navigate(context.Background(), server.URL+"/redirect")
	require.NoError(t, err)

	// 4. Verify Final State (Session follows redirects manually)
	expectedURL := server.URL + "/final"
	assert.Equal(t, expectedURL, s.GetCurrentURL())
}

func TestSession_NavigationTimeout(t *testing.T) {
	// Configure a very short request timeout
	timeoutDuration := 500 * time.Millisecond
	cfg := config.NewDefaultConfig()
	cfg.Network.NavigationTimeout = timeoutDuration
	cfg.Network.PostLoadWait = 0

	logger := zap.NewNop()
	jsRuntime := jsexec.NewRuntime()
	s, err := session.NewSession(context.Background(), cfg, schemas.DefaultPersona, logger, jsRuntime, nil)
	require.NoError(t, err)
	s.Initialize(context.Background(), nil, "", "")
	defer s.Close(context.Background())

	// Server that intentionally delays the response.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second) // Longer than the timeout.
		fmt.Fprintln(w, `<html><body>Slow response</body></html>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	// Navigation should fail with a timeout error.
	err = s.Navigate(ctx, server.URL)
	require.Error(t, err)
	// The client's transport/request error is typically wrapped as a net/url.Error or context.DeadlineExceeded.
	assert.True(t, strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline exceeded"), "Error should be a timeout/deadline error")
}

// -- DOM Interaction and State Tests --

func TestSession_ExecuteTypeAndSelect_StateUpdate(t *testing.T) {
	// Tests that interactions modify the internal DOM representation (crucial for Pure Go implementation)

	// 1. Setup Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `
		<html><body>
			<input type="text" id="username" value="old_value">
			<select id="options">
				<option value="A">A</option>
				<option value="B" selected="selected">B</option>
			</select>
			<textarea id="area">initial area text</textarea>
		</body></html>`)
	}))
	defer server.Close()

	// 2. Setup Session and Navigate
	s, _, _ := setupTestSession(t)
	defer s.Close(context.Background())
	s.Navigate(context.Background(), server.URL)

	// 3. Execute Type (Input)
	newText := "test_user"
	err := s.Type(context.Background(), `//input[@id="username"]`, newText)
	require.NoError(t, err)

	// 4. Execute Type (Textarea)
	areaText := "lorem ipsum"
	err = s.Type(context.Background(), `//textarea[@id="area"]`, areaText)
	require.NoError(t, err)

	// 5. Execute Select
	newValue := "A"
	err = s.ExecuteSelect(context.Background(), `//select[@id="options"]`, newValue)
	require.NoError(t, err)

	// 6. Verify DOM State Update using GetDOMSnapshot
	snapshot, _ := s.GetDOMSnapshot(context.Background())
	content, _ := io.ReadAll(snapshot)
	domContent := string(content)

	// Check input value update (attribute)
	assert.Contains(t, domContent, fmt.Sprintf(`value="%s"`, newText))

	// Check textarea update (inner text)
	assert.Contains(t, domContent, fmt.Sprintf(`<textarea id="area">%s</textarea>`, areaText))

	// Check select option update (selected attribute)
	assert.Contains(t, domContent, `<option value="A" selected="selected">A</option>`)
	assert.NotContains(t, domContent, `<option value="B" selected="selected">B</option>`)
}

func TestSession_ExecuteClick_Consequences(t *testing.T) {
	// Tests click consequences: Navigation, Form Submission, Checkbox/Radio state change.
	
	// 1. Setup Mock Server
	var finalURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.String()
		if r.Method == http.MethodPost {
			r.ParseForm()
			fmt.Fprintln(w, `<html><title>POST Success</title></html>`)
		} else if r.URL.Path == "/start" {
			fmt.Fprintln(w, `
				<html><body>
					<a id="navLink" href="/target?id=123">Navigate</a>
					<input type="checkbox" id="check1">
					<input type="checkbox" id="check2" checked="checked">
					<input type="radio" name="r_group" id="radio1" value="r1">
					<input type="radio" name="r_group" id="radio2" value="r2" checked="checked">
					<form action="/post_target" method="POST" id="form1"><button type="submit" id="submitBtn">Submit</button></form>
				</body></html>`)
		} else {
			fmt.Fprintln(w, `<html><title>Final</title></html>`)
		}
	}))
	defer server.Close()

	// 2. Setup Session and Navigate
	s, _, _ := setupTestSession(t)
	defer s.Close(context.Background())
	s.Navigate(context.Background(), server.URL+"/start")

	// -- Test 1: Navigation Click (<a>) --
	t.Run("AnchorClick", func(t *testing.T) {
		require.NoError(t, s.ExecuteClick(context.Background(), `//a[@id="navLink"]`, 0, 0))
		assert.Equal(t, server.URL+"/target?id=123", s.GetCurrentURL())
		
		// Navigate back to the start page for the next tests
		s.Navigate(context.Background(), server.URL+"/start")
	})

	// -- Test 2: Checkbox Toggle --
	t.Run("CheckboxToggle", func(t *testing.T) {
		// Click #check1 (unchecked -> checked)
		require.NoError(t, s.ExecuteClick(context.Background(), `//input[@id="check1"]`, 0, 0))
		// Click #check2 (checked -> unchecked)
		require.NoError(t, s.ExecuteClick(context.Background(), `//input[@id="check2"]`, 0, 0))

		snapshot, _ := s.GetDOMSnapshot(context.Background())
		domContent, _ := io.ReadAll(snapshot)
		
		assert.Contains(t, string(domContent), `<input type="checkbox" id="check1" checked="checked">`)
		assert.NotContains(t, string(domContent), `<input type="checkbox" id="check2" checked="checked">`)
	})
	
	// -- Test 3: Radio Selection --
	t.Run("RadioSelect", func(t *testing.T) {
		// Click #radio1 (r1 is unchecked, r2 is checked)
		require.NoError(t, s.ExecuteClick(context.Background(), `//input[@id="radio1"]`, 0, 0))

		snapshot, _ := s.GetDOMSnapshot(context.Background())
		domContent, _ := io.ReadAll(snapshot)
		
		// Verify #radio1 is now checked
		assert.Contains(t, string(domContent), `<input type="radio" name="r_group" id="radio1" value="r1" checked="checked">`)
		// Verify #radio2 is now unchecked
		assert.NotContains(t, string(domContent), `<input type="radio" name="r_group" id="radio2" value="r2" checked="checked">`)
	})

	// -- Test 4: Form Submission Click --
	t.Run("SubmitButtonClick", func(t *testing.T) {
		require.NoError(t, s.ExecuteClick(context.Background(), `//button[@id="submitBtn"]`, 0, 0))
		assert.Equal(t, server.URL+"/post_target", s.GetCurrentURL())
		assert.Contains(t, finalURL, "/post_target")
	})
}

func TestSession_FormSubmission_POST(t *testing.T) {
	// Tests the explicit Submit function which finds the form parent.
	
	// 1. Setup Mock Server to receive form data
	var submittedData string
	var contentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprintln(w, `
			<html><body>
				<form action="/submit" method="POST" id="loginForm">
					<input type="text" name="username" value="testuser">
					<input type="checkbox" name="remember" checked>
					<input type="submit" value="Login">
				</form>
				<div id="wrapper">
					<input type="text" id="trigger" name="password" value="secret_pass">
				</div>
			</body></html>`)
		} else if r.Method == http.MethodPost && r.URL.Path == "/submit" {
			r.ParseForm()
			submittedData = r.Form.Encode()
			contentType = r.Header.Get("Content-Type")
			fmt.Fprintln(w, `<html><title>Success</title></html>`)
		}
	}))
	defer server.Close()

	// 2. Setup Session and Navigate
	s, _, _ := setupTestSession(t)
	defer s.Close(context.Background())
	s.Navigate(context.Background(), server.URL)

	// 3. Update a field first (to verify serialization uses the current DOM state)
	require.NoError(t, s.Type(context.Background(), `//input[@name="username"]`, "new_user"))

	// 4. Submit the form using an element *inside* the form
	err := s.Submit(context.Background(), `//input[@type="submit"]`)
	require.NoError(t, err)

	// 5. Verify Submission
	// Note: url.Values.Encode() sorts keys alphabetically
	expectedData := "remember=on&username=new_user"
	assert.Equal(t, expectedData, submittedData)
	assert.Equal(t, "application/x-www-form-urlencoded", contentType)

	// 6. Verify Navigation occurred
	expectedURL := server.URL + "/submit"
	assert.Equal(t, expectedURL, s.GetCurrentURL())
}

// -- Utility and Artifact Tests --

func TestSession_ExecuteScript_GojaIntegration(t *testing.T) {
	s, _, _ := setupTestSession(t)
	defer s.Close(context.Background())
	
	// 1. Test simple return value
	var result float64
	err := s.ExecuteScript(context.Background(), "3 + 4", &result)
	require.NoError(t, err)
	assert.Equal(t, float64(7), result)

	// 2. Test complex return object (requires JSON marshalling/unmarshalling)
	var obj map[string]string
	script := `JSON.stringify({"status": "ok", "message": "hello"});`
	err = s.ExecuteScript(context.Background(), script, &obj)
	require.NoError(t, err)
	assert.Equal(t, "ok", obj["status"])
	assert.Equal(t, "hello", obj["message"])

	// 3. Test error handling
	err = s.ExecuteScript(context.Background(), "throw new Error('JS Fail')", nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "JS Fail")
}

func TestSession_ArtifactCollection(t *testing.T) {
	// Tests HAR and final DOM collection.
	
	// 1. Setup Mock Server that provides a body.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Test HAR</title></head><body>Content Body</body></html>`)
	}))
	defer server.Close()

	// 2. Setup Session
	s, _, findingsChan := setupTestSession(t)
	defer s.Close(context.Background())
	
	// 3. Navigate (creates HAR entry)
	require.NoError(t, s.Navigate(context.Background(), server.URL))
	
	// 4. Add Finding (sends to channel)
	testFinding := schemas.Finding{Title: "XSS Detected", Severity: "High"}
	s.AddFinding(testFinding)

	// 5. Collect Artifacts
	artifacts, err := s.CollectArtifacts(context.Background())
	require.NoError(t, err)
	require.NotNil(t, artifacts)
	
	// Verify Final Artifacts
	assert.Equal(t, server.URL, artifacts.FinalURL)
	assert.Contains(t, artifacts.FinalDOM, "<title>Test HAR</title>")
	
	// Verify HAR
	require.NotNil(t, artifacts.HAR)
	require.Len(t, artifacts.HAR.Log.Entries, 1, "Expected one HAR entry for navigation")
	assert.Equal(t, server.URL, artifacts.HAR.Log.Entries[0].Request.URL)
	assert.Contains(t, artifacts.HAR.Log.Entries[0].Response.Content.Text, "Content Body")
	
	// Verify Findings
	select {
	case found := <-findingsChan:
		assert.Equal(t, testFinding.Title, found.Title)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Timed out waiting for finding to be reported")
	}
}

func TestSession_CombineContext(t *testing.T) {
	// Tests the crucial utility for concurrent context management.
	
	parentCtx, parentCancel := context.WithCancel(context.Background())
	secondaryCtx, secondaryCancel := context.WithCancel(context.Background())
	defer parentCancel()
	defer secondaryCancel()

	// 1. Test cancellation from parent context
	combined1, cancel1 := session.CombineContext(parentCtx, secondaryCtx)
	parentCancel()
	<-combined1.Done()
	assert.ErrorIs(t, combined1.Err(), context.Canceled)
	cancel1() // Cleanup

	// 2. Test cancellation from secondary context
	parentCtx2 := context.Background() // New, non-canceled parent
	secondaryCtx2, secondaryCancel2 := context.WithCancel(context.Background())
	combined2, cancel2 := session.CombineContext(parentCtx2, secondaryCtx2)
	secondaryCancel2()
	<-combined2.Done()
	assert.ErrorIs(t, combined2.Err(), context.Canceled)
	cancel2() // Cleanup
}
