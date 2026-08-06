FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/paygate ./cmd/api

FROM gcr.io/distroless/base-debian12
COPY --from=build /out/paygate /paygate
EXPOSE 8080
ENTRYPOINT ["/paygate"]
