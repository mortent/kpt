package util

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	DefaultMainReferenceName plumbing.ReferenceName = "refs/heads/main"
	OriginName               string                 = "origin"

	branchPrefixInLocalRepo  = "refs/remotes/" + OriginName + "/"
	branchPrefixInRemoteRepo = "refs/heads/"
	tagsPrefixInLocalRepo    = "refs/tags/"
	tagsPrefixInRemoteRepo   = "refs/tags/"

	branchRefSpec config.RefSpec = config.RefSpec("+" + branchPrefixInRemoteRepo + "*:" + branchPrefixInLocalRepo + "*")
	tagRefSpec    config.RefSpec = config.RefSpec("+" + tagsPrefixInRemoteRepo + "*:" + tagsPrefixInLocalRepo + "*")

	draftsPrefix             = "drafts/"
	draftsPrefixInLocalRepo  = branchPrefixInLocalRepo + draftsPrefix
	draftsPrefixInRemoteRepo = branchPrefixInRemoteRepo + draftsPrefix

	proposedPrefix             = "proposed/"
	proposedPrefixInLocalRepo  = branchPrefixInLocalRepo + proposedPrefix
	proposedPrefixInRemoteRepo = branchPrefixInRemoteRepo + proposedPrefix

	deletionProposedPrefix             = "deletionProposed/"
	deletionProposedPrefixInLocalRepo  = branchPrefixInLocalRepo + deletionProposedPrefix
	deletionProposedPrefixInRemoteRepo = branchPrefixInRemoteRepo + deletionProposedPrefix
)

var (
	// The default fetch spec contains both branches and tags.
	// This enables push of a tag which will automatically update
	// its local reference, avoiding explicitly setting of refs.
	DefaultFetchSpec []config.RefSpec = []config.RefSpec{
		branchRefSpec,
		tagRefSpec,
	}

	// DO NOT USE for fetches. Used for reverse reference mapping only.
	reverseFetchSpec []config.RefSpec = []config.RefSpec{
		config.RefSpec(branchPrefixInLocalRepo + "*:" + branchPrefixInRemoteRepo + "*"),
		config.RefSpec(tagsPrefixInLocalRepo + "*:" + tagsPrefixInRemoteRepo + "*"),
	}
)

func IsDraftBranchNameInLocal(n plumbing.ReferenceName) bool {
	return strings.HasPrefix(n.String(), draftsPrefixInLocalRepo)
}

func IsProposedBranchNameInLocal(n plumbing.ReferenceName) bool {
	return strings.HasPrefix(n.String(), proposedPrefixInLocalRepo)
}

func RefInRemoteFromRefInLocal(n plumbing.ReferenceName) (plumbing.ReferenceName, error) {
	return translateReference(n, reverseFetchSpec)
}

func translateReference(n plumbing.ReferenceName, specs []config.RefSpec) (plumbing.ReferenceName, error) {
	for _, spec := range specs {
		if spec.Match(n) {
			return spec.Dst(n), nil
		}
	}
	return "", fmt.Errorf("cannot translate reference %s", n)
}
