FROM registry.ci.openshift.org/openshift/release:golang-1.19 AS builder
WORKDIR /go/src/github.com/vrutkovs/kaas
COPY . .
RUN go mod vendor && go build -o ./kaas ./cmd/kaas


FROM registry.access.redhat.com/ubi8/ubi-minimal:8.5
COPY --from=builder /go/src/github.com/vrutkovs/kaas/kaas /bin/kaas
COPY --from=builder /go/src/github.com/vrutkovs/kaas/html /srv/html
WORKDIR /srv
ENTRYPOINT ["/bin/kaas"]
