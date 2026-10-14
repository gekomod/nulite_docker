# Nulite Docker

Panel WWW i historia danych pompy Nulite działają w Dockerze. ESP8266 pozostaje modułem RS485/Modbus, udostępnia JSON i nadal obsługuje OTA.

## Wymagania
- Docker Engine + Compose v2
- ESP8266 i serwer Docker w tej samej lub routowalnej sieci
- Firmware ESP udostępniający `GET /api/data`

## Szybki start
```sh
cp .env.example .env
docker compose up -d --build
docker compose logs -f nulite
```
Otwórz `http://ADRES_SERWERA:8088`. Przy pierwszym uruchomieniu pojawi się kreator połączenia. Podaj adres ESP8266, np. `http://192.168.31.131`, i wybierz „Sprawdź połączenie i zapisz”. Serwer zweryfikuje endpoint `/api/data` i zachowa adres w SQLite. Konfiguracja przeżywa restart kontenera i aktualizacje dzięki wolumenowi `nulite_data`. Jeśli `ESP_URL` ustawisz w środowisku, zostanie użyty jako wartość początkowa.

## API serwera
- `GET /api/v1/health` — healthcheck
- `GET /api/v1/config` — stan konfiguracji (bez ujawniania pełnego adresu)
- `POST /api/v1/config` — weryfikuje i zapisuje `{"esp_url":"http://adres-esp"}`
- `GET /api/v1/status` — status połączenia i dane ESP
- `GET /api/v1/data` — JSON z ESP `/api/data`
- `GET /api/v1/history?limit=100` — próbki zapisane do SQLite
- `GET /api/v1/esp/api/data` — jawne proxy odczytowe

## Automatyczne budowanie
GitHub Actions buduje obraz i paczkę po każdym pushu do `main`; wynik jest dostępny jako artefakt workflow. Tag wersji, np. `v1.0.0`, publikuje paczkę w GitHub Releases. Obraz jest publikowany w GHCR. Firmware ma własny workflow w repozytorium `gekomod/nulite-esp8266`.

## Ręczne budowanie paczki
Na maszynie z Dockerem:
```sh
chmod +x scripts/build-package.sh
./scripts/build-package.sh 1.0.0
```
Powstanie `dist/nulite-docker-1.0.0.tar.gz`, `dist/compose.yml` i `dist/.env.example`.

Na docelowym serwerze:
```sh
docker load -i nulite-docker-1.0.0.tar.gz
cp .env.example .env
docker compose -f compose.yml up -d
```
Otwórz panel i wykonaj kreator pierwszego uruchomienia.

## Ważne
Panel nie wysyła poleceń zapisu Modbus. Aktualizacja OTA nadal odbywa się na ESP. JSON API nie usuwa problemu przepełnienia sprzętowego bufora UART — odbiór i buforowanie ramek muszą być niezawodne po stronie firmware. Zmiany należy traktować jako pierwszą wersję integracji do sprawdzenia przez GitHub Actions i uruchomienie na docelowym sprzęcie.
