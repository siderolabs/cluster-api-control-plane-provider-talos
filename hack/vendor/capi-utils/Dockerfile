# syntax = docker/dockerfile-upstream:1.19.0-labs

# THIS FILE WAS AUTOMATICALLY GENERATED, PLEASE DO NOT EDIT.
#
# Generated on 2025-11-18T10:24:05Z by kres e1d6dac.

ARG TOOLCHAIN=scratch

# cleaned up specs and compiled versions
FROM scratch AS generate

FROM ghcr.io/siderolabs/ca-certificates:v1.11.0 AS image-ca-certificates

FROM ghcr.io/siderolabs/fhs:v1.11.0 AS image-fhs

# runs markdownlint
FROM docker.io/oven/bun:1.3.1-alpine AS lint-markdown
WORKDIR /src
RUN bun i markdownlint-cli@0.45.0 sentences-per-line@0.3.0
COPY .markdownlint.json .
COPY ./README.md ./README.md
RUN bunx markdownlint --ignore "CHANGELOG.md" --ignore "**/node_modules/**" --ignore '**/hack/chglog/**' --rules sentences-per-line .

# base toolchain image
FROM --platform=${BUILDPLATFORM} ${TOOLCHAIN} AS toolchain
RUN apk --update --no-cache add bash build-base curl jq protoc protobuf-dev

# build tools
FROM --platform=${BUILDPLATFORM} toolchain AS tools
ENV GO111MODULE=on
ARG CGO_ENABLED
ENV CGO_ENABLED=${CGO_ENABLED}
ARG GOTOOLCHAIN
ENV GOTOOLCHAIN=${GOTOOLCHAIN}
ARG GOEXPERIMENT
ENV GOEXPERIMENT=${GOEXPERIMENT}
ENV GOPATH=/go
ARG DEEPCOPY_VERSION
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg go install github.com/siderolabs/deep-copy@${DEEPCOPY_VERSION} \
	&& mv /go/bin/deep-copy /bin/deep-copy
ARG GOLANGCILINT_VERSION
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCILINT_VERSION} \
	&& mv /go/bin/golangci-lint /bin/golangci-lint
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg go install golang.org/x/vuln/cmd/govulncheck@latest \
	&& mv /go/bin/govulncheck /bin/govulncheck
ARG GOFUMPT_VERSION
RUN go install mvdan.cc/gofumpt@${GOFUMPT_VERSION} \
	&& mv /go/bin/gofumpt /bin/gofumpt

# tools and sources
FROM tools AS base
WORKDIR /src
COPY go.mod go.mod
COPY go.sum go.sum
RUN cd .
RUN --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg go mod download
RUN --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg go mod verify
COPY ./cmd ./cmd
COPY ./pkg ./pkg
RUN --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg go list -mod=readonly all >/dev/null

# builds capi-darwin-amd64
FROM base AS capi-darwin-amd64-build
COPY --from=generate / /
WORKDIR /src/cmd/capi
ARG GO_BUILDFLAGS
ARG GO_LDFLAGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg GOARCH=amd64 GOOS=darwin go build ${GO_BUILDFLAGS} -ldflags "${GO_LDFLAGS}" -o /capi-darwin-amd64

# builds capi-darwin-arm64
FROM base AS capi-darwin-arm64-build
COPY --from=generate / /
WORKDIR /src/cmd/capi
ARG GO_BUILDFLAGS
ARG GO_LDFLAGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg GOARCH=arm64 GOOS=darwin go build ${GO_BUILDFLAGS} -ldflags "${GO_LDFLAGS}" -o /capi-darwin-arm64

# builds capi-linux-amd64
FROM base AS capi-linux-amd64-build
COPY --from=generate / /
WORKDIR /src/cmd/capi
ARG GO_BUILDFLAGS
ARG GO_LDFLAGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg GOARCH=amd64 GOOS=linux go build ${GO_BUILDFLAGS} -ldflags "${GO_LDFLAGS}" -o /capi-linux-amd64

# builds capi-linux-arm64
FROM base AS capi-linux-arm64-build
COPY --from=generate / /
WORKDIR /src/cmd/capi
ARG GO_BUILDFLAGS
ARG GO_LDFLAGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg GOARCH=arm64 GOOS=linux go build ${GO_BUILDFLAGS} -ldflags "${GO_LDFLAGS}" -o /capi-linux-arm64

# builds capi-linux-armv7
FROM base AS capi-linux-armv7-build
COPY --from=generate / /
WORKDIR /src/cmd/capi
ARG GO_BUILDFLAGS
ARG GO_LDFLAGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg GOARCH=arm GOARM=7 GOOS=linux go build ${GO_BUILDFLAGS} -ldflags "${GO_LDFLAGS}" -o /capi-linux-armv7

# builds capi-windows-amd64.exe
FROM base AS capi-windows-amd64.exe-build
COPY --from=generate / /
WORKDIR /src/cmd/capi
ARG GO_BUILDFLAGS
ARG GO_LDFLAGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg GOARCH=amd64 GOOS=windows go build ${GO_BUILDFLAGS} -ldflags "${GO_LDFLAGS}" -o /capi-windows-amd64.exe

# runs gofumpt
FROM base AS lint-gofumpt
RUN FILES="$(gofumpt -l .)" && test -z "${FILES}" || (echo -e "Source code is not formatted with 'gofumpt -w .':\n${FILES}"; exit 1)

# runs golangci-lint
FROM base AS lint-golangci-lint
WORKDIR /src
COPY .golangci.yml .
ENV GOGC=50
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/root/.cache/golangci-lint,id=capi-utils/root/.cache/golangci-lint,sharing=locked --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg golangci-lint run --config .golangci.yml

# runs golangci-lint fmt
FROM base AS lint-golangci-lint-fmt-run
WORKDIR /src
COPY .golangci.yml .
ENV GOGC=50
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/root/.cache/golangci-lint,id=capi-utils/root/.cache/golangci-lint,sharing=locked --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg golangci-lint fmt --config .golangci.yml
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/root/.cache/golangci-lint,id=capi-utils/root/.cache/golangci-lint,sharing=locked --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg golangci-lint run --fix --issues-exit-code 0 --config .golangci.yml

# runs govulncheck
FROM base AS lint-govulncheck
WORKDIR /src
COPY --chmod=0755 hack/govulncheck.sh ./hack/govulncheck.sh
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg ./hack/govulncheck.sh ./...

# runs unit-tests with race detector
FROM base AS unit-tests-race
WORKDIR /src
ARG TESTPKGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg --mount=type=cache,target=/tmp,id=capi-utils/tmp CGO_ENABLED=1 go test -race ${TESTPKGS}

# runs unit-tests
FROM base AS unit-tests-run
WORKDIR /src
ARG TESTPKGS
RUN --mount=type=cache,target=/root/.cache/go-build,id=capi-utils/root/.cache/go-build --mount=type=cache,target=/go/pkg,id=capi-utils/go/pkg --mount=type=cache,target=/tmp,id=capi-utils/tmp go test -covermode=atomic -coverprofile=coverage.txt -coverpkg=${TESTPKGS} ${TESTPKGS}

FROM scratch AS capi-darwin-amd64
COPY --from=capi-darwin-amd64-build /capi-darwin-amd64 /capi-darwin-amd64

FROM scratch AS capi-darwin-arm64
COPY --from=capi-darwin-arm64-build /capi-darwin-arm64 /capi-darwin-arm64

FROM scratch AS capi-linux-amd64
COPY --from=capi-linux-amd64-build /capi-linux-amd64 /capi-linux-amd64

FROM scratch AS capi-linux-arm64
COPY --from=capi-linux-arm64-build /capi-linux-arm64 /capi-linux-arm64

FROM scratch AS capi-linux-armv7
COPY --from=capi-linux-armv7-build /capi-linux-armv7 /capi-linux-armv7

FROM scratch AS capi-windows-amd64.exe
COPY --from=capi-windows-amd64.exe-build /capi-windows-amd64.exe /capi-windows-amd64.exe

# clean golangci-lint fmt output
FROM scratch AS lint-golangci-lint-fmt
COPY --from=lint-golangci-lint-fmt-run /src .

FROM scratch AS unit-tests
COPY --from=unit-tests-run /src/coverage.txt /coverage-unit-tests.txt

FROM capi-linux-${TARGETARCH} AS capi

FROM scratch AS capi-all
COPY --from=capi-darwin-amd64 / /
COPY --from=capi-darwin-arm64 / /
COPY --from=capi-linux-amd64 / /
COPY --from=capi-linux-arm64 / /
COPY --from=capi-linux-armv7 / /
COPY --from=capi-windows-amd64.exe / /

FROM scratch AS image-capi
ARG TARGETARCH
COPY --from=capi capi-linux-${TARGETARCH} /capi
COPY --from=image-fhs / /
COPY --from=image-ca-certificates / /
LABEL org.opencontainers.image.source=https://github.com/siderolabs/capi-utils
ENTRYPOINT ["/capi"]

