#!/bin/bash
# ============================================================================
# Generate TLS certificates for GoFTPd master/slave
# Generates ECDSA P-384 keys for TLSv1.3 TLS_AES_256_GCM_SHA384
# ============================================================================

echo "Generating TLS certificates for GoFTPd..."
echo ""

mkdir -p etc/certs
cd etc/certs

# Generate CA (ECDSA P-384 for AES-256-GCM)
openssl ecparam -genkey -name secp384r1 -out ca.key
openssl req -new -x509 -sha384 -days 3650 -key ca.key -out ca.crt \
  -subj "/CN=GoFTPd-CA/O=GoFTPd"

# Generate server cert (master + FTP clients)
openssl ecparam -genkey -name secp384r1 -out server.key
openssl req -new -sha384 -key server.key -out server.csr \
  -subj "/CN=GoFTPd-Master/O=GoFTPd"
openssl x509 -req -sha384 -days 3650 -in server.csr \
  -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt

# Generate slave cert
openssl ecparam -genkey -name secp384r1 -out client.key
openssl req -new -sha384 -key client.key -out client.csr \
  -subj "/CN=GoFTPd-Slave/O=GoFTPd"
openssl x509 -req -sha384 -days 3650 -in client.csr \
  -CA ca.crt -CAkey ca.key -CAcreateserial -out client.crt

rm -f *.csr *.srl
cd ../..

echo ""
echo "Certificates generated in etc/certs/"
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
