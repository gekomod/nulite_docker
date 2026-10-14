#!/usr/bin/env sh
set -eu
VERSION="${1:-dev}"
mkdir -p dist
docker build --pull -t "nulite-docker:${VERSION}" .
docker save "nulite-docker:${VERSION}" | gzip -9 > "dist/nulite-docker-${VERSION}.tar.gz"
cp compose.yml dist/compose.yml
cp .env.example dist/.env.example
cat > dist/README-DEPLOY.txt <<EOF
Nulite Docker ${VERSION}

1. Rozpakuj paczke.
2. Skopiuj .env.example do .env i ustaw ESP_URL na adres ESP8266.
3. Uruchom: docker load -i nulite-docker-${VERSION}.tar.gz
4. Uruchom: docker compose -f compose.yml up -d
5. Panel: http://ADRES_SERWERA:${NULITE_PORT:-8088}

Dane historii sa przechowywane w wolumenie Docker nulite_data.
EOF
echo "Gotowe: dist/nulite-docker-${VERSION}.tar.gz + compose.yml + .env.example"
