FROM golang:1.21-bookworm as build

RUN mkdir /src
WORKDIR /src
COPY . .

RUN go build -o /go/bin/gpushare-device-plugin-v2 cmd/nvidia/main.go
RUN go build -o /go/bin/kubectl-inspect-gpushare-v2 cmd/inspect/*.go

FROM debian:bullseye-slim

ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=utility

COPY --from=build /go/bin/gpushare-device-plugin-v2 /usr/bin/gpushare-device-plugin-v2

COPY --from=build /go/bin/kubectl-inspect-gpushare-v2 /usr/bin/kubectl-inspect-gpushare-v2

CMD ["gpushare-device-plugin-v2","-logtostderr"]
