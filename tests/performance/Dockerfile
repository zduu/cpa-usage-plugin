ARG GO_IMAGE=golang:1.26.6-bookworm
ARG CPA_IMAGE=eceasy/cli-proxy-api@sha256:02b3bb12d866cfc8255b5ee066eee86ad425669da6a79de5b55569eddbe48ba8
FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
# Match the stock CPA image's glibc ABI. Alpine/musl binaries cannot run in
# this Debian image, even when their CPU architecture is correct.
RUN gcc --version
WORKDIR /src/go
COPY go/go.mod go/go.sum ./
RUN go mod download
COPY go/ ./
RUN go build -buildmode=c-shared -buildvcs=false -trimpath -ldflags='-s -w' -o /out/usage-dashboard-zduu.so .
# Go's ELF c-shared linker binds internal C symbols locally. Keep that
# property when SQLite is embedded, even if the host also embeds SQLite.
RUN readelf -d /out/usage-dashboard-zduu.so | grep -q '(SYMBOLIC)'
RUN go test -c -o /out/performance.test
WORKDIR /src/stock-host
COPY tests/stock-host/ ./
RUN go build -buildvcs=false -trimpath -o /out/stock-host .

FROM ${CPA_IMAGE}
COPY --from=build /out/ /performance/
# The unmodified CPA entrypoint remains in use. Mount an isolated config/data
# directory and select /performance as plugins.dir in that config.
