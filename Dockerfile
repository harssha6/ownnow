FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /ownnow ./cmd/ownnow

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /ownnow /usr/local/bin/ownnow
EXPOSE 8080
ENTRYPOINT ["ownnow", "serve"]
