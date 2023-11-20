FROM golang:1.21-bookworm as build

RUN mkdir /src
WORKDIR /src

COPY go.sum .
COPY go.mod .

RUN go mod download

COPY pkg pkg
COPY cmd cmd

RUN --mount=type=cache,target=/root/.cache/go-build go build -o /gpushare-device-plugin-v2 ./cmd/nvidia
RUN --mount=type=cache,target=/root/.cache/go-build go build -o /kubectl-inspect-gpushare-v2 ./cmd/inspect

FROM debian:bullseye-slim

ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=utility

WORKDIR /

COPY --from=build /gpushare-device-plugin-v2 /gpushare-device-plugin-v2

COPY --from=build /kubectl-inspect-gpushare-v2 /usr/bin/kubectl-inspect-gpushare-v2

CMD ["gpushare-device-plugin-v2","-logtostderr"]
