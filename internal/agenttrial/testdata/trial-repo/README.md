# paygate

A small billing API: accounts, invoices, and the arithmetic between
them.

## Layout

    cmd/api            HTTP entry point
    internal/billing   invoice totals and discounts
    internal/httpapi   route handlers
    internal/store     account storage
    migrations/        SQL schema, applied in filename order

## Development

    go build ./...
    go test ./...
    golangci-lint run

The service listens on `:8080` by default; set `ADDR` to override.

## Container

    docker build -t paygate .
    docker run --rm -p 8080:8080 paygate
