FROM golang:1.17-alpine AS build
LABEL stage=raft.build
WORKDIR /app
COPY . .
RUN go mod download && \
    go build -o /raft && \
    cd admin && \
    go build -o /admin

FROM alpine:3.14
LABEL stage=raft.final
RUN apk add --no-cache iptables
WORKDIR /app
COPY --from=build /raft /admin /app/run.sh ./
ENTRYPOINT ["./run.sh"]

