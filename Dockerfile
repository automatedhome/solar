FROM arm32v7/golang:stretch

COPY qemu-arm-static /usr/bin/
WORKDIR /go/src/github.com/automatedhome/solar
COPY . .
RUN go build -o solar cmd/main.go

FROM arm32v7/busybox:1.30-glibc

COPY --from=0 /go/src/github.com/automatedhome/solar/solar /usr/bin/solar

ENTRYPOINT [ "/usr/bin/solar" ]
