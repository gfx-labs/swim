release:
	go run github.com/guilhem/bump@v0.2.3 patch
	unset GITLAB_TOKEN && go run github.com/goreleaser/goreleaser/v2 release --clean
