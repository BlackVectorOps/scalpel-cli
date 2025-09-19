// internal/browser/interactor_test.go
package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xkilldash9x/scalpel-cli/api/schemas"
)

func TestInteractor(t *testing.T) {
	t.Run("FormInteraction", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t)
		session := fixture.Session

		submissionChan := make(chan url.Values, 1)

		server := createTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/submit" {
				r.ParseForm()
				// Copy the form to prevent data races, as the original r.Form can be modified.
				copiedForm := make(url.Values)
				for k, v := range r.Form {
					newVal := make([]string, len(v))
					copy(newVal, v)
					copiedForm[k] = newVal
				}
				// Ensure the channel write doesn't block if the test already timed out.
				select {
				case submissionChan <- copiedForm:
				default:
					t.Log("Form submission received late, channel full or closed.")
				}

				fmt.Fprintln(w, `<html><body>Form processed</body></html>`)
				return
			}

			// This is the HTML for the form page.
			fmt.Fprintln(w, `
					<html>
						<body>
							<form action="/submit" method="POST">
								<input type="text" name="username" id="userField">
								<input type="password" name="password">
								<select name="color">
									<option value="">Select...</option>
									<option value="red">Red</option>
									<option value="blue">Blue</option>
								</select>
								<button type="submit" id="submitBtn">Submit</button>
								<input type="reset" value="Clear">
								<input type="text" readonly value="Readonly">
							</form>
							<button disabled>Inactive</button>
						</body>
					</html>
				`)
		}))
		defer server.Close()

		// Use a timed context for the entire test's browser operations.
		// We can use context.Background() because the implementation (Navigate, Interact)
		// now uses CombineContext() internally.
		ctx, cancel := context.WithTimeout(context.Background(), interactorTestTimeout)
		defer cancel()

		err := session.Navigate(ctx, server.URL)
		require.NoError(t, err)

		config := schemas.InteractionConfig{
			MaxDepth:                2,
			MaxInteractionsPerDepth: 5,
			InteractionDelayMs:      50,
			PostInteractionWaitMs:   200,
		}

		err = session.Interact(ctx, config)
		require.NoError(t, err, "Interaction phase failed")

		// Wait for the form data to be received on the channel or time out.
		var formData url.Values
		select {
		case formData = <-submissionChan:
			// Form data received successfully.
		case <-ctx.Done():
			t.Fatal("Test timed out waiting for form submission (context done)")
		case <-time.After(10 * time.Second):
			// Added an absolute timeout as a fallback safety measure.
			t.Fatal("Absolute timeout waiting for form submission")
		}

		assert.Equal(t, "Test User", formData.Get("username"), "Username payload mismatch")
		assert.Equal(t, "ScalpelTest123!", formData.Get("password"), "Password payload mismatch")
		selectedColor := formData.Get("color")
		assert.True(t, selectedColor == "red" || selectedColor == "blue", "Select interaction failed or selected invalid option")
	})

	t.Run("DynamicContentHandling", func(t *testing.T) {
		t.Parallel()
		fixture := newTestFixture(t)
		session := fixture.Session

		server := createTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// This page has a button that reveals another button after a short delay.
			fmt.Fprintln(w, `
					<html>
						<body>
							<div id="status">Initial</div>
							<button id="revealBtn" onclick="reveal()">Reveal Content</button>
							<div id="dynamicContent" style="display:none;">
								<button id="dynamicBtn" onclick="updateStatus()">Interact Dynamically</button>
							</div>
							<script>
								function reveal() {
									setTimeout(() => {
										document.getElementById('dynamicContent').style.display = 'block';
										document.getElementById('status').innerText = 'Revealed';
									}, 150);
								}
								function updateStatus() {
									document.getElementById('status').innerText = 'Dynamic Interaction Success';
								}
							</script>
						</body>
					</html>
				`)
		}))
		defer server.Close()

		// Main operation context for the test. Can be background based.
		ctx, cancel := context.WithTimeout(context.Background(), interactorTestTimeout)
		defer cancel()

		err := session.Navigate(ctx, server.URL)
		require.NoError(t, err)

		config := schemas.InteractionConfig{
			MaxDepth:                3,
			MaxInteractionsPerDepth: 2,
			InteractionDelayMs:      50,
			PostInteractionWaitMs:   250, // Increased slightly to allow for JS timeout.
		}

		err = session.Interact(ctx, config)
		require.NoError(t, err)

		// Use assert.Eventually to poll for the asynchronous UI change.
		assert.Eventually(t, func() bool {
			var finalStatus string

			// The check requires a valid chromedp context. We derive it from the session context.
			// We also apply a short timeout for the check itself.
			// CombineContext ensures the check stops if the main test context (ctx) is done.
			checkCtx, checkCancel := CombineContext(session.GetContext(), ctx)
			defer checkCancel()

			// Apply the short timeout to the combined context.
			timeoutCheckCtx, timeoutCancel := context.WithTimeout(checkCtx, 2*time.Second)
			defer timeoutCancel()

			// This runs inside the poll loop.
			err := chromedp.Run(timeoutCheckCtx, chromedp.Text("#status", &finalStatus, chromedp.ByQuery))
			if err != nil {
				// Return false to continue polling if the error is transient.
				return false
			}
			return finalStatus == "Dynamic Interaction Success"
		}, 10*time.Second, 100*time.Millisecond, "Interactor failed to interact with dynamic content")
	})
}