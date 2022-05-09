FROM arm32v7/golang:stretch as builder
 
COPY qemu-arm-static /usr/bin/
WORKDIR /go/src/github.com/automatedhome/solar
COPY . .
RUN make build

FROM arm32v7/busybox:1.30-glibc

COPY --from=builder /go/src/github.com/automatedhome/solar/solar /usr/bin/solar
COPY --from=builder /go/src/github.com/automatedhome/solar/config.yaml /config.yaml

HEALTHCHECK --timeout=5s --start-period=1m \
  CMD wget --quiet --tries=1 --spider http://localhost:7001/health || exit 1

ENTRYPOINT [ "/usr/bin/solar" ]
