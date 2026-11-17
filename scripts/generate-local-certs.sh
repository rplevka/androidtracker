#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cert_dir="$project_root/traefik/certs"
domain="${TRACKER_DOMAIN:-}"

if [[ -z "$domain" ]]; then
  echo "Set TRACKER_DOMAIN to the server's LAN IP or DNS name." >&2
  echo "Example: TRACKER_DOMAIN=192.0.2.10 $0" >&2
  exit 2
fi
if [[ "$domain" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]]; then
  san="IP:$domain"
elif [[ "$domain" =~ ^[A-Za-z0-9.-]+$ && "$domain" != .* && "$domain" != *. ]]; then
  san="DNS:$domain"
else
  echo "TRACKER_DOMAIN must be an IPv4 address or simple DNS name." >&2
  exit 2
fi
command -v openssl >/dev/null || { echo "openssl not found" >&2; exit 1; }

mkdir -p "$cert_dir"
chmod 700 "$cert_dir"
ca_key="$cert_dir/local-ca-key.pem"
ca_cert="$cert_dir/local-ca-cert.pem"
server_key="$cert_dir/server-key.pem"
server_cert="$cert_dir/server-cert.pem"
if { [[ -e "$ca_key" ]] && [[ ! -e "$ca_cert" ]]; } || { [[ ! -e "$ca_key" ]] && [[ -e "$ca_cert" ]]; }; then
  echo "CA key/certificate pair is incomplete; restore both or remove both before regenerating." >&2
  exit 1
fi

umask 077
if [[ ! -e "$ca_key" ]]; then
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$ca_key"
  openssl req -x509 -new -key "$ca_key" -sha256 -days 3650 \
    -subj "/CN=Kidstracker Local Development CA" \
    -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "subjectKeyIdentifier=hash" \
    -out "$ca_cert"
fi

openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$server_key"
openssl req -new -key "$server_key" -subj "/CN=$domain" -out "$cert_dir/server.csr"
cat > "$cert_dir/server.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=$san
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
EOF
openssl x509 -req -in "$cert_dir/server.csr" -CA "$ca_cert" -CAkey "$ca_key" \
  -set_serial "0x$(openssl rand -hex 16)" -out "$server_cert" -days 825 -sha256 -extfile "$cert_dir/server.ext"
rm -f "$cert_dir/server.csr" "$cert_dir/server.ext" "$cert_dir/local-ca-cert.srl"
chmod 600 "$ca_key" "$server_key"
chmod 644 "$ca_cert" "$server_cert"

cat > "$project_root/traefik/dynamic/routes.yml" <<EOF
http:
  routers:
    kidstracker-http:
      rule: "Host(\`$domain\`)"
      entryPoints:
        - web
      middlewares:
        - https-redirect
      service: kidstracker
    kidstracker-https:
      rule: "Host(\`$domain\`)"
      entryPoints:
        - websecure
      middlewares:
        - secure-headers
      service: kidstracker
      tls: {}
  middlewares:
    https-redirect:
      redirectScheme:
        scheme: https
        port: "8443"
        permanent: true
    secure-headers:
      headers:
        stsSeconds: 31536000
        frameDeny: true
        contentTypeNosniff: true
        referrerPolicy: strict-origin-when-cross-origin
        permissionsPolicy: "geolocation=(),camera=(),microphone=()"
  services:
    kidstracker:
      loadBalancer:
        servers:
          - url: http://api:8080
EOF

echo "Generated local HTTPS certificate for $domain."
echo "Private CA/server keys are in $cert_dir; keep them private and out of Git."
echo "CA certificate to install in the Android user's credential store: $ca_cert"
