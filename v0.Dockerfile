FROM registry.ci.openshift.org/ocp/builder:rhel-8-golang-1.20-openshift-4.15 as builder

WORKDIR /src/github.com/openshift/operator-framework-tooling
COPY ./cmd/v0/main.go go.mod ./
RUN go build -o v0 -mod=mod ./...

FROM quay.io/centos/centos:stream8

RUN dnf install -y git glibc make
COPY --from=builder /src/github.com/openshift/operator-framework-tooling/v0 /usr/bin/bumper
COPY --from=builder /usr/lib/golang/bin/go /usr/bin/go
COPY --from=builder /usr/lib/golang /usr/lib/golang

ENTRYPOINT ["bumper"]