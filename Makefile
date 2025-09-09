release:
	go run github.com/guilhem/bump@latest patch
	unset GITLAB_TOKEN && goreleaser release --clean
