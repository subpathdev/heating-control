FROM golang:1.26.8-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /heating-control ./cmd/heating-control

# A static binary that only talks to the in-cluster broker needs nothing else.
FROM scratch
COPY --from=build /heating-control /heating-control
USER 65532:65532
ENTRYPOINT ["/heating-control"]
