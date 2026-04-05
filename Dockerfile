FROM debian:bookworm-slim AS usearch-builder
RUN apt-get update && apt-get install -y --no-install-recommends \
        gcc g++ cmake git ca-certificates \
    && git clone --depth 1 --branch v2.24.0 https://github.com/unum-cloud/usearch.git /tmp/usearch \
    && cd /tmp/usearch \
    && cmake -B build_release \
        -DCMAKE_BUILD_TYPE=Release \
        -DUSEARCH_BUILD_LIB_C=ON \
        -DUSEARCH_BUILD_TEST_C=OFF \
        -DUSEARCH_BUILD_BENCH_CPP=OFF \
        -DUSEARCH_BUILD_TEST_CPP=OFF \
    && cmake --build build_release --config Release -j$(nproc)

FROM golang:1.26-bookworm AS builder
COPY --from=usearch-builder /tmp/usearch/build_release/libusearch_c.so /usr/local/lib/
COPY --from=usearch-builder /tmp/usearch/c/usearch.h /usr/local/include/
RUN apt-get update && apt-get install -y gcc libsqlite3-dev ca-certificates && ldconfig
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 CGO_CFLAGS="-I/usr/local/include" CGO_LDFLAGS="-L/usr/local/lib -lusearch_c" go build -tags sqlite_fts5 -o /imprint ./cmd/imprint

FROM debian:bookworm-slim
COPY --from=usearch-builder /tmp/usearch/build_release/libusearch_c.so /usr/local/lib/
RUN apt-get update && apt-get install -y ca-certificates && ldconfig && rm -rf /var/lib/apt/lists/*
COPY --from=builder /imprint /usr/local/bin/imprint
COPY --from=builder /src/LICENSE /usr/local/share/doc/imprint/LICENSE
COPY --from=builder /src/prompts /etc/imprint/prompts
ENV IMPRINT_CONFIG=/etc/imprint/config.toml
ENTRYPOINT ["imprint"]
