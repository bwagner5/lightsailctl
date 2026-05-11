FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 go build -o /out/hello .

FROM alpine:3.20
COPY --from=build /out/hello /usr/local/bin/hello
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/hello"]
