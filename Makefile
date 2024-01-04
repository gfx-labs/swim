

release:
	unset GITLAB_TOKEN
	goreleaser --clean
	goreleaser release
