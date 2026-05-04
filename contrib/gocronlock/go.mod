module github.com/ubgo/lock/contrib/gocronlock

go 1.24

// Cross-module test dependencies (filelock, memlock) are picked up via
// the repo-root go.work for local dev; when published, the require
// lines below pin the published versions.
require (
	github.com/go-co-op/gocron/v2 v2.21.1
	github.com/ubgo/lock v0.1.0
)

require (
	github.com/ubgo/lock/filelock v0.0.0-20260504055553-35be8b73a431
	github.com/ubgo/lock/memlock v0.0.0-20260504055553-35be8b73a431
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
)
