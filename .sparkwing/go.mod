module sparkwing-pipelines

go 1.26.0

require (
	github.com/fergusstrange/embedded-postgres v1.34.0
	github.com/google/go-containerregistry v0.22.1
	github.com/sparkwing-dev/sparks-core/templates v0.33.5
	github.com/sparkwing-dev/sparkwing v0.67.0
	go.yaml.in/yaml/v3 v3.0.4
	golang.org/x/mod v0.41.0
	golang.org/x/sys v0.48.0
)

require (
	github.com/docker/cli v29.7.2+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.9.3 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/lib/pq v1.10.9 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/image-spec v1.1.1 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/xi2/xz v0.0.0-20171230120015-48954b6210f8 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	gotest.tools/v3 v3.5.2 // indirect
)

// The pipelines tree is consumed as the same module path the SDK
// itself ships, so the require above is a placeholder; this replace
// pins it to the parent checkout (the sparkwing repo root). The
// pattern follows the standard "consumer .sparkwing/ uses a local
// replace during development" convention; here the parent IS the
// SDK rather than a sibling.
replace github.com/sparkwing-dev/sparkwing => ..
