FROM golang:1.26-bookworm AS builder
RUN apt-get update && apt-get install -y gcc libsqlite3-dev wget ca-certificates \
    && wget -q https://github.com/unum-cloud/USearch/releases/download/v2.24.0/usearch_linux_amd64_2.24.0.deb \
    && dpkg -i usearch_linux_amd64_2.24.0.deb || apt-get install -f -y \
    && rm -f usearch_linux_amd64_2.24.0.deb
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 CGO_LDFLAGS="-lusearch_c" go build -tags sqlite_fts5 -o /imprint ./cmd/imprint

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates wget \
    && wget -q https://github.com/unum-cloud/USearch/releases/download/v2.24.0/usearch_linux_amd64_2.24.0.deb \
    && dpkg -i usearch_linux_amd64_2.24.0.deb || apt-get install -f -y \
    && rm -f usearch_linux_amd64_2.24.0.deb \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /imprint /usr/local/bin/imprint
COPY --from=builder /src/LICENSE /usr/local/share/doc/imprint/LICENSE
COPY --from=builder /src/prompts /etc/imprint/prompts
ENV IMPRINT_CONFIG=/etc/imprint/config.toml
ENTRYPOINT ["imprint"]
