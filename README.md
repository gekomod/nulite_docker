# Nulite Docker

Panel WWW dla pompy Nulite. Interfejs i archiwizacja działają w Dockerze; ESP8266 pozostaje modułem RS485/Modbus i nadal obsługuje OTA firmware.

## Wymagania
- Docker Engine z Compose v2
- ESP8266 i serwer Docker w tej samej sieci LAN (lub routowalnych sieciach)
- Firmware ESP z działającym endpointem `GET /api/data`

## Szybki start
```sh
cp .env.example .env
# Ustaw ESP_URL w .env na aktualny adres ESP8266
docker compose up -d --build
docker compose logs -f nulite
```

Panel: `http://ADRES_SERWERA:8088` (port zmienisz przez `NULITE_PORT`).

## API serwera
- `GET /api/v1/health` — healthcheck
- `GET /api/v1/status` — status połączenia i aktualna odpowiedź ESP
- `GET /api/v1/data` — proxy JSON z ESP `/api/data`
- `GET /api/v1/history?limit=100` — ostatnie próbki zapisane do SQLite
- `GET /api/v1/esp/api/data` — jawne proxy tylko do dozwolonych tras odczytu

Serwer cyklicznie odczytuje `/api/data` z ESP i zapisuje pomiary oraz diagnostykę w SQLite. Baza jest w wolumenie `nulite_data`, więc przetrwa odtworzenie kontenera.

## Budowanie paczki do przeniesienia na inny serwer
Na maszynie z Dockerem:
```sh
chmod +x scripts/build-package.sh
./scripts/build-package.sh 1.0.0
```
Powstanie `dist/nulite-docker-1.0.0.tar.gz`, `dist/compose.yml` i `dist/.env.example`.

Na docelowym serwerze przenieś te pliki, rozpakuj archiwum jeśli było dodatkowo spakowane i uruchom:
```sh
docker load -i nulite-docker-1.0.0.tar.gz
cp .env.example .env
# ustaw ESP_URL
docker compose -f compose.yml up -d
```

## GitHub Actions
Workflow `.github/workflows/docker-release.yml` buduje obraz i paczkę. Uruchom ręcznie z Actions, aby pobrać artefakt, lub wypchnij tag `v1.0.0`, aby utworzyć GitHub Release z paczką.

## Ważne
To pierwsza wersja serwera/panelu. Endpoint ESP `/api/data` jest już używany przez firmware, natomiast OTA nadal wykonuje się na ESP. Na razie serwer nie wysyła komend Modbus i nie próbuje aktualizować firmware samodzielnie. Ustawienia zmieniające parametry UART wymagają osobnego, świadomie dodanego endpointu firmware. Przepełnienie UART trzeba diagnozować i naprawiać po stronie ESP niezależnie od przeniesienia strony do Dockera.
