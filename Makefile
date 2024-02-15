release:
	unset GITLAB_TOKEN
	goreleaser release --clean
