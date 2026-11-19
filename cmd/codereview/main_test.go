package main

import "testing"

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
