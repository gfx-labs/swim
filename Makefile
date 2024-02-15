release:
	go run github.com/guilhem/bump patch
	unset GITLAB_TOKEN && goreleaser release --clean
