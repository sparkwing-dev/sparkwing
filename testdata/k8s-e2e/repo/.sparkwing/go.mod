module sparkwing-k8s-e2e-pipelines

go 1.26.0

require github.com/sparkwing-dev/sparkwing v0.68.0

require (
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

// Exercise the checkout SDK while release tooling keeps the require pin coherent.
replace github.com/sparkwing-dev/sparkwing => ../../../..
