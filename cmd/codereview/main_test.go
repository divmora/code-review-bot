package main

import (
	"os"
	"testing"
)

func TestRefFromCIBitbucketPipelines(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("CI_MERGE_REQUEST_IID", "")
	t.Setenv("BITBUCKET_PR_ID", "42")
	t.Setenv("BITBUCKET_WORKSPACE", "acme")
	t.Setenv("BITBUCKET_REPO_SLUG", "web")
	ref, ok := refFromCI()
	if !ok || ref.Platform != "bitbucket" || ref.Repo != "acme/web" || ref.Number != 42 {
		t.Fatalf("ref = %+v %v", ref, ok)
	}
	t.Setenv("BITBUCKET_PR_ID", "") // branch pipelines have no PR
	if _, ok := refFromCI(); ok {
		t.Error("no PR outside pull-request pipelines")
	}
}

func TestLogOutputFollowsResultMarker(t *testing.T) {
	t.Setenv("CODEREVIEW_RESULT_MARKER", "")
	if logOutput() != os.Stderr {
		t.Error("logs go to stderr by default")
	}
	// The console's Kubernetes executor sets the marker and reads pod logs,
	// which merge both streams: logs must share stdout with the result.
	t.Setenv("CODEREVIEW_RESULT_MARKER", "@@CODEREVIEW-RESULT-x@@")
	if logOutput() != os.Stdout {
		t.Error("with a result marker, logs go to stdout")
	}
}
