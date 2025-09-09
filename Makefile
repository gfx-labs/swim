release:
	go run github.com/guilhem/bump@latest patch
	unset GITLAB_TOKEN && go run github.com/goreleaser/goreleaser@v2.12.0 release --clean
