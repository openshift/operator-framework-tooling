FROM registry.ci.openshift.org/ocp/builder:rhel-9-golang-1.21-openshift-4.16 as builder

WORKDIR /src/github.com/openshift/operator-framework-tooling
COPY ./cmd/ ./cmd/
COPY ./pkg/ ./pkg/
COPY go.mod ./
RUN go build -o v1 -mod=mod ./cmd/v1/...

FROM quay.io/centos/centos:stream8

RUN dnf install -y git glibc make
COPY --from=builder /src/github.com/openshift/operator-framework-tooling/v1 /usr/bin/bumper
COPY --from=builder /usr/lib/golang/bin/go /usr/bin/go
COPY --from=builder /usr/lib/golang /usr/lib/golang

ENTRYPOINT ["bumper"]