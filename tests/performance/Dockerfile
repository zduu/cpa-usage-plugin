ARG GO_IMAGE=golang:1.26.4-alpine
ARG CPA_IMAGE=eceasy/cli-proxy-api@sha256:02b3bb12d866cfc8255b5ee066eee86ad425669da6a79de5b55569eddbe48ba8
FROM ${GO_IMAGE} AS build
RUN apk add --no-cache build-base
WORKDIR /src/go
COPY go/go.mod go/go.sum ./
RUN go mod download
COPY go/ ./
RUN go build -buildmode=c-shared -buildvcs=false -trimpath -ldflags='-s -w' -o /out/usage-dashboard-zduu.so .
RUN go test -c -o /out/performance.test
WORKDIR /src/stock-host
COPY tests/stock-host/ ./
RUN go build -buildvcs=false -trimpath -o /out/stock-host .

FROM ${CPA_IMAGE}
COPY --from=build /out/ /performance/
# The unmodified CPA entrypoint remains in use. Mount an isolated config/data
# directory and select /performance as plugins.dir in that config.
