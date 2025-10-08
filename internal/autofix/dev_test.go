// internal/autofix/dev_test.go
package autofix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/go-github/v58/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"github.com/xkilldash9x/scalpel-cli/internal/config"
	"github.com/xkilldash9x/scalpel-cli/internal/mocks"
)

// SetupHermeticEnv creates an isolated environment for E2E testing the Developer workflow.
// It includes a mock GitHub server and a local Git repository acting as the remote origin.
func SetupHermeticEnv(t *testing.T) (*Developer, *mocks.MockLLMClient, string) {
	t.Helper()

	// Check prerequisites
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Skipping E2E test: 'git' command not found in PATH.")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("Skipping E2E test: 'go' command not found in PATH.")
	}

	// 1. Setup Mock GitHub API Server
	mockGHServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-gh-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user":
			user := github.User{Login: github.String("TestBot")}
			json.NewEncoder(w).Encode(user)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			var newPR github.NewPullRequest
			json.NewDecoder(r.Body).Decode(&newPR)
			pr := github.PullRequest{
				Number:  github.Int(1),
				HTMLURL: github.String("http://mockgithub.com/pull/1"),
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(&pr)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(mockGHServer.Close)

	// 2. Setup Local Git "Remote" Repository (The Origin)
	remotePath := t.TempDir()
	repo, err := git.PlainInit(remotePath, false)
	require.NoError(t, err)

	w, _ := repo.Worktree()

	require.NoError(t, os.WriteFile(filepath.Join(remotePath, "go.mod"), []byte("module test/repo\n\ngo 1.21\n"), 0644))

	pkgDir := filepath.Join(remotePath, "pkg", "calc")
	require.NoError(t, os.MkdirAll(pkgDir, 0755))
	buggyCode := `package calc

// Divide panics if b is 0.
func Divide(a, b int) int {
	return a / b // Line 5
}
`
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "calc.go"), []byte(buggyCode), 0644))

	w.Add(".")
	_, err = w.Commit("Initial commit", &git.CommitOptions{
		Author: &object.Signature{Name: "Test Author", Email: "author@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	// 3. Setup Mocks and Developer instance
	logger := zaptest.NewLogger(t)
	mockLLM := new(mocks.MockLLMClient)
	cfg := &config.AutofixConfig{
		Enabled: true,
		GitHub: config.GitHubConfig{
			Token:      "mock-gh-token",
			RepoOwner:  "test-owner",
			RepoName:   "test-repo",
			BaseBranch: "main",
		},
		Git: config.GitConfig{
			AuthorName:  "Autofix Bot",
			AuthorEmail: "bot@example.com",
		},
	}

	httpClient := &http.Client{
		Transport: &authTransport{
			Token: cfg.GitHub.Token,
			Wrap:  http.DefaultTransport,
		},
	}

	ghClient := github.NewClient(httpClient)
	ghClient.BaseURL, _ = url.Parse(mockGHServer.URL + "/")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err = ghClient.Users.Get(ctx, "")
	require.NoError(t, err, "Failed to authenticate with mock GitHub server")

	dev := &Developer{
		logger:       logger.Named("test-dev"),
		llmClient:    mockLLM,
		cfg:          cfg,
		githubClient: ghClient,
		gitAuth:      nil,
		ghConfig:     &cfg.GitHub,
		gitConfig:    &cfg.Git,
	}

	return dev, mockLLM, remotePath
}

// authTransport is a helper for injecting the auth token into requests.
type authTransport struct {
	Token string
	Wrap  http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.Token)
	return t.Wrap.RoundTrip(req)
}

// DE LUX FEATURE: TestDeveloper_E2E_TDD_Workflow simulates the full cycle: Fail -> Patch -> Pass -> PR.
func TestDeveloper_E2E_TDD_Workflow(t *testing.T) {

	dev, mockLLM, remotePath := SetupHermeticEnv(t)

	// --- Test Inputs ---
	// 1. The Report
	report := PostMortem{
		IncidentID:   "DIVZERO-12345678",
		FilePath:     "pkg/calc/calc.go",
		LineNumber:   5,
		PanicMessage: "runtime error: integer divide by zero",
	}

	// 2. The Patch (Analysis)
	// FIX: The hunk header must match the file's actual content and line numbers.
	patch := `--- a/pkg/calc/calc.go
+++ b/pkg/calc/calc.go
@@ -3,4 +3,7 @@
 // Divide panics if b is 0.
 func Divide(a, b int) int {
+	if b == 0 {
+		return 0 // Handle division by zero
+	}
 	return a / b // Line 5
 }
`
	analysis := &AnalysisResult{
		Patch:       patch,
		Explanation: "Added safety check.",
		Confidence:  0.99,
	}

	// 3. Configure LLM Mock for Test Generation
	generatedTest := `package calc

import "testing"

func TestAutofix_Incident_DIVZERO(t *testing.T) {
	// This test is designed to reproduce the panic
	Divide(10, 0)
}
`
	// Configure the mock LLM response
	mockLLM.On("Generate", mock.Anything, mock.Anything).Return(generatedTest, nil).Once()

	// --- Execution ---
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	// Step 1: Prepare Workspace (Manually cloning the local remote)
	workspacePath := t.TempDir()
	_, err := git.PlainCloneContext(ctx, workspacePath, false, &git.CloneOptions{
		URL: remotePath, // Clone from the local "remote" path
	})
	require.NoError(t, err, "Failed to clone local remote repository")

	// Step 2: Generate Test Case
	wsTestFilePath := filepath.Join(workspacePath, "pkg/calc/calc_test.go")
	testFuncName, err := dev.generateTestCase(ctx, report, wsTestFilePath)
	require.NoError(t, err, "generateTestCase failed")
	assert.Equal(t, "TestAutofix_Incident_DIVZERO", testFuncName)

	// Step 3: Run Test (TDD Red - Expect Failure/Panic)
	err = dev.runSpecificTest(ctx, workspacePath, wsTestFilePath, testFuncName, true)
	require.NoError(t, err, "TDD Red phase failed: Test did not panic as expected")

	// Step 4: Apply Patch
	err = dev.applyPatch(ctx, workspacePath, analysis.Patch)
	require.NoError(t, err, "applyPatch failed")

	// Verify patch applied to the filesystem
	patchedContent, _ := os.ReadFile(filepath.Join(workspacePath, report.FilePath))
	assert.Contains(t, string(patchedContent), "if b == 0 {")

	// Step 5: Run Test (TDD Green - Expect Pass)
	err = dev.runSpecificTest(ctx, workspacePath, wsTestFilePath, testFuncName, false)
	require.NoError(t, err, "TDD Green phase failed: Test failed after patch")

	// Step 6: Run Full Suite (Regression Check)
	err = dev.runFullTestSuite(ctx, workspacePath)
	require.NoError(t, err, "Regression check failed: Full test suite failed")

	// Step 7: Create Pull Request
	err = dev.createPullRequest(ctx, workspacePath, report, analysis)

	// Handle potential issues with pushing to local remotes in specific CI environments
	if err != nil && (strings.Contains(err.Error(), "authentication required") || strings.Contains(err.Error(), "credentials")) {
		t.Skipf("Skipping PR push test: Local git environment requires authentication for file:// remotes: %v", err)
	}
	require.NoError(t, err, "createPullRequest failed")

	// Verification: Check if the branch was pushed to the remote
	remoteRepo, err := git.PlainOpen(remotePath)
	require.NoError(t, err)

	expectedBranchPrefix := fmt.Sprintf("fix/autofix-%s", report.IncidentID[:8])
	refs, err := remoteRepo.References()
	require.NoError(t, err)

	foundBranch := false
	refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() && strings.HasPrefix(ref.Name().Short(), expectedBranchPrefix) {
			foundBranch = true
		}
		return nil
	})
	assert.True(t, foundBranch, "Fix branch was not pushed to the remote repository")
}

// TestDeveloper_HelperFunctions tests utility functions.
func TestDeveloper_HelperFunctions(t *testing.T) {
	t.Parallel()
	t.Run("extractTestFuncName", func(t *testing.T) {
		assert.Equal(t, "TestMyFeature", extractTestFuncName("func TestMyFeature(t *testing.T) {}"))
		assert.Equal(t, "", extractTestFuncName("func Helper() {}"))
	})

	t.Run("cleanLLMCodeOutput", func(t *testing.T) {
		code := "func main() {}"
		assert.Equal(t, code, cleanLLMCodeOutput("```go\n"+code+"\n```"))
		assert.Equal(t, code, cleanLLMCodeOutput("```golang\n"+code+"\n```"))
		assert.Equal(t, code, cleanLLMCodeOutput("  \n"+code+"\n  "))
	})
}
