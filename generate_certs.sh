#!/bin/bash
# ============================================================================
# Generate TLS certificates for GoFTPd master/slave
# Generates ECDSA P-384 keys for TLSv1.3 TLS_AES_256_GCM_SHA384
# Usage:
#   ./generate_certs.sh [site-name]
#   GOFTPD_CERT_NAME="My FTPd" ./generate_certs.sh
# ============================================================================

set -eu

SITE_NAME="${1:-${GOFTPD_CERT_NAME:-GoFTPd}}"
OUT_DIR="etc/certs"
CA_CN="${SITE_NAME} Root CA"
SERVER_CN="${SITE_NAME} FTP"
CLIENT_CN="${SITE_NAME} Slave"
ORG="${SITE_NAME}"

echo "Generating TLS certificates for ${SITE_NAME}..."
echo ""

mkdir -p "${OUT_DIR}"
cd "${OUT_DIR}"

# Generate CA (ECDSA P-384 for AES-256-GCM)
openssl ecparam -genkey -name secp384r1 -out ca.key
openssl req -new -x509 -sha384 -days 3650 -key ca.key -out ca.crt \
  -subj "/CN=${CA_CN}/O=${ORG}"

# Generate server cert (master + FTP clients)
openssl ecparam -genkey -name secp384r1 -out server.key
openssl req -new -sha384 -key server.key -out server.csr \
  -subj "/CN=${SERVER_CN}/O=${ORG}"
openssl x509 -req -sha384 -days 3650 -in server.csr \
  -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt

# Generate slave cert
openssl ecparam -genkey -name secp384r1 -out client.key
openssl req -new -sha384 -key client.key -out client.csr \
  -subj "/CN=${CLIENT_CN}/O=${ORG}"
openssl x509 -req -sha384 -days 3650 -in client.csr \
  -CA ca.crt -CAkey ca.key -CAcreateserial -out client.crt

rm -f *.csr *.srl
cd ../..

echo ""
echo "Certificates generated in ${OUT_DIR}/"
echo ""
echo "  Site name:   ${SITE_NAME}"
echo "  Issued by:   ${CA_CN}"
echo "  Server cert: ${SERVER_CN}"
echo "  Slave cert:  ${CLIENT_CN}"
echo ""
echo "  ECDSA P-384 keys -> TLSv1.3 TLS_AES_256_GCM_SHA384"
echo ""
echo "  ca.crt      - CA certificate"
echo "  server.crt  - Master/FTP certificate"
echo "  server.key  - Master/FTP private key"
echo "  client.crt  - Slave certificate"
echo "  client.key  - Slave private key"
echo ""
echo "Update etc/config.yml:"
echo "  tls_cert: ./etc/certs/server.crt"
echo "  tls_key:  ./etc/certs/server.key"
