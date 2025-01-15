FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.23-openshift-4.19 as builder

WORKDIR /src/github.com/openshift/operator-framework-tooling
COPY ./cmd/ ./cmd/
COPY ./pkg/ ./pkg/
COPY go.mod ./
RUN go build -o v0 -mod=mod ./cmd/v0/...
RUN go install -mod=mod github.com/bwplotka/bingo@latest

FROM registry.ci.openshift.org/ocp/4.19:base-rhel9

RUN dnf install -y git glibc make
COPY --from=builder /src/github.com/openshift/operator-framework-tooling/v0 /usr/bin/bumper
COPY --from=builder /usr/lib/golang/bin/go /usr/bin/go
COPY --from=builder /usr/lib/golang /usr/lib/golang
COPY --from=builder /go/bin/bingo /usr/bin/bingo

ENTRYPOINT ["bumper"]
