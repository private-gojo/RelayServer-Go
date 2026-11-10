# ============================================================
# GOJO X-RELAY (RelayServer-Go) — MULTI-STAGE DOCKERFILE
# ============================================================

# ------------------------------------------------------------
# STAGE 1: Go Multi-Core Bridge Builder (x-relay-bridge)
# ------------------------------------------------------------
FROM golang:1.27.3-alpine AS go-builder

WORKDIR /src
COPY go.mod ./
COPY relay-ssh/ ./relay-ssh/
COPY relay-security/ ./relay-security/
COPY relay-utilities/ ./relay-utilities/

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/x-relay-bridge ./relay-ssh

# ------------------------------------------------------------
# STAGE 2: Rust UDPGW Builder (badvpn-udpgw)
# ------------------------------------------------------------
FROM rust:1.96-alpine3.24@sha256:a41f7740f8b45d45795624eec13a8b42263cc700f19f7e4e86e04d3dda08a479 AS udpgw-builder

WORKDIR /build
COPY relay-rust/ ./
RUN cargo test --locked && cargo build --locked --release

# ------------------------------------------------------------
# STAGE 3: Dropbear Builder (Modern Cipher, KEX, MAC & Hostkey Suite)
# ------------------------------------------------------------
FROM alpine:3.24.3 AS dropbear-builder

RUN apk add --no-cache \
    build-base=0.5-r4 \
    wget=1.25.0-r3 \
    zlib-dev=1.4-r0 \
    linux-headers=7.0.0-r1 \
    openssl-dev=3.5.9-r0

RUN wget https://matt.ucc.asn.au/dropbear/releases/dropbear-2026.94.tar.bz2 && \
    tar -xf dropbear-2026.94.tar.bz2 && \
    cd dropbear-2026.94 && \
    grep -Fq '#define LOCAL_IDENT "SSH-2.0-dropbear" IDENT_VERSION_PART' src/sysoptions.h && \
    sed -i 's|#define LOCAL_IDENT "SSH-2.0-dropbear" IDENT_VERSION_PART|#define LOCAL_IDENT "SSH-2.0-" DROPBEAR_VERSION|' src/sysoptions.h && \
    sed -i 's|^#define MAX_BANNER_SIZE [0-9]*|#define MAX_BANNER_SIZE 4096|' src/sysoptions.h && \
    grep -Fq '#define MAX_BANNER_SIZE 4096' src/sysoptions.h && \
    printf '%s\n' \
      '#define DROPBEAR_VERSION "OpenSSH_10.6p2 Ubuntu-2ubuntu3.6"' \
      '#define DROPBEAR_SMALL_CODE 0' \
      '#define DROPBEAR_USER_ALGO_LIST 1' \
      '#define DROPBEAR_AES128 1' \
      '#define DROPBEAR_AES256 1' \
      '#define DROPBEAR_3DES 0' \
      '#define DROPBEAR_CHACHA20POLY1305 1' \
      '#define DROPBEAR_ENABLE_CTR_MODE 1' \
      '#define DROPBEAR_ENABLE_GCM_MODE 1' \
      '#define DROPBEAR_ENABLE_CBC_MODE 0' \
      '#define DROPBEAR_SHA1_HMAC 0' \
      '#define DROPBEAR_SHA2_256_HMAC 1' \
      '#define DROPBEAR_SHA2_512_HMAC 0' \
      '#define DROPBEAR_SHA1_96_HMAC 0' \
      '#define DROPBEAR_RSA 1' \
      '#define DROPBEAR_RSA_SHA1 0' \
      '#define DROPBEAR_DSS 0' \
      '#define DROPBEAR_ECDSA 1' \
      '#define DROPBEAR_ED25519 1' \
      '#define DROPBEAR_DH_GROUP14_SHA1 0' \
      '#define DROPBEAR_DH_GROUP14_SHA256 1' \
      '#define DROPBEAR_DH_GROUP16 0' \
      '#define DROPBEAR_CURVE25519 1' \
      '#define DROPBEAR_SNTRUP761 1' \
      '#define DROPBEAR_MLKEM768 1' \
      '#define DROPBEAR_ECDH 1' \
      '#define DROPBEAR_DH_GROUP1 0' \
      '#define MAX_UNAUTH_PER_IP 512' \
      '#define MAX_UNAUTH_CLIENTS 512' \
      '#define AUTH_TIMEOUT 60' > localoptions.h && \
    ./configure && \
    make PROGRAMS="dropbear dropbearkey" && \
    make PROGRAMS="dropbear dropbearkey" install

# ------------------------------------------------------------
# STAGE 4: Production Runtime Image (No Python Overhead)
# ------------------------------------------------------------
FROM alpine:3.24.3

COPY --from=go-builder /out/x-relay-bridge /usr/local/bin/x-relay-bridge
COPY --from=dropbear-builder /usr/local/sbin/dropbear /usr/local/sbin/dropbear
COPY --from=dropbear-builder /usr/local/bin/dropbearkey /usr/local/bin/dropbearkey
COPY --from=udpgw-builder /build/target/release/badvpn-udpgw /usr/local/bin/badvpn-udpgw

RUN apk add --no-cache \
    supervisor=4.3.0-r1 \
    bash=5.3.9-r1 \
    shadow=4.18.0-r1 \
    tzdata=2026f-r0 \
    zlib=1.4-r0 \
    nginx=1.30.6-r0 \
    nginx-mod-stream=1.30.6-r0 \
    netcat-openbsd=1.234.1-r0

RUN mkdir -p /etc/dropbear /var/log/supervisor /etc/x-relay /run/nginx && \
    cp /usr/share/zoneinfo/Asia/Manila /etc/localtime && \
    echo "Asia/Manila" > /etc/timezone && \
    rm -f /etc/nginx/http.d/default.conf /etc/nginx/conf.d/default.conf

COPY relay-web/banner.txt /etc/x-relay/banner.txt
COPY relay-utilities/supervisord.conf /etc/supervisord.conf
COPY relay-utilities/entrypoint.sh /entrypoint.sh
COPY relay-security/nginx.conf.template /etc/x-relay/nginx.conf.template
COPY relay-utilities/wait-for-bridge.sh /etc/x-relay/wait-for-bridge.sh

RUN chmod +x /entrypoint.sh /etc/x-relay/wait-for-bridge.sh /usr/local/bin/x-relay-bridge

RUN test -x /usr/sbin/nginx && \
    test -x /usr/local/bin/x-relay-bridge && \
    test -x /usr/local/sbin/dropbear && \
    test -x /usr/local/bin/dropbearkey && \
    test -x /usr/local/bin/badvpn-udpgw && \
    test -x /usr/bin/supervisord

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD nc -z 127.0.0.1 8081 && nc -z 127.0.0.1 7300 || exit 1

ENTRYPOINT ["/entrypoint.sh"]
