# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/formance2blnk ./cmd/formance2blnk

FROM gcr.io/distroless/static:nonroot
LABEL org.opencontainers.image.source=https://github.com/otyang/formance2blnk
COPY --from=build /out/formance2blnk /usr/local/bin/formance2blnk
WORKDIR /work
USER nonroot:nonroot
ENTRYPOINT ["formance2blnk"]
CMD ["help"]
