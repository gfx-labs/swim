VERSION=v1.1.1

release:
	unset GITLAB_TOKEN
	goreleaser release --clean
