FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.24-openshift-4.21 as builder

WORKDIR /src/github.com/openshift/operator-framework-tooling
COPY ./cmd/ ./cmd/
COPY ./pkg/ ./pkg/
COPY go.mod ./
RUN go build -o v1 -mod=mod ./cmd/v1/...
RUN go install -mod=mod github.com/bwplotka/bingo@v0.9.0

FROM registry.ci.openshift.org/ocp/4.21:base-rhel9

RUN dnf install -y git glibc make
COPY --from=builder /src/github.com/openshift/operator-framework-tooling/v1 /usr/bin/bumper
COPY --from=builder /usr/lib/golang/bin/go /usr/bin/go
COPY --from=builder /go/bin/bingo /usr/bin/bingo
COPY --from=builder /usr/lib/golang /usr/lib/golang

ENTRYPOINT ["bumper"]
