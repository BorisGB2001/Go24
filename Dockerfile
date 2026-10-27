# boris.gb 2026-10-11 13:32 - сборка go24: Go собирается в контейнере, на хосте Go не нужен
FROM golang:1-alpine AS build
WORKDIR /src
# boris.gb 2026-10-11 14:39 - go.sum в репозитории: зависимости качаются по зафиксированным версиям и кэшируются отдельным слоем
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# boris.gb 2026-10-27 18:07 - фон видео в звонке (GO24DEV-778): MediaPipe tasks-vision (Apache-2.0) и модель selfie segmenter
# скачиваются при сборке по зафиксированной версии и sha256 и встраиваются в static; в git их нет (около 10 МБ)
ADD --checksum=sha256:3122ba7a70d414ffbb3a1bd194c370594a1bf1c90e467c375a5b8bd25be2c004 https://registry.npmjs.org/@mediapipe/tasks-vision/-/tasks-vision-0.10.14.tgz /tmp/tasks-vision.tgz
ADD --checksum=sha256:191ac9529ae506ee0beefa6b2c945a172dab9d07d1e802a290a4e4038226658b https://storage.googleapis.com/mediapipe-models/image_segmenter/selfie_segmenter/float16/1/selfie_segmenter.tflite internal/web/static/js/vendor/mediapipe/selfie_segmenter.tflite
RUN mkdir -p internal/web/static/js/vendor/mediapipe/wasm && tar xzf /tmp/tasks-vision.tgz -C /tmp && \
    cp /tmp/package/vision_bundle.mjs internal/web/static/js/vendor/mediapipe/ && \
    cp /tmp/package/wasm/vision_wasm_internal.js /tmp/package/wasm/vision_wasm_internal.wasm internal/web/static/js/vendor/mediapipe/wasm/
# boris.gb 2026-10-27 18:11 - версия из VERSION зашивается в сборку: её показывает «Настройки → Состояние системы» (GO24DEV-780)
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X go24/internal/sysinfo.Version=$(cat VERSION)" -o /out/go24 ./cmd/go24

FROM alpine:3.22
# boris.gb 2026-10-24 23:03 - poppler-utils: pdftotext читает текст PDF для поиска по содержимому на Диске (GO24DEV-224)
# boris.gb 2026-10-25 23:55 - postgresql17-client: pg_dump для «Резервного копирования» (GO24DEV-569), версия — как у сервера postgres:17
# boris.gb 2026-10-27 21:22 - ffmpeg: go24-worker вырезает звук из видеозаписи звонка для расшифровки Whisper (GO24DEV-798)
RUN apk add --no-cache ca-certificates tzdata poppler-utils postgresql17-client ffmpeg && adduser -D -H -u 10001 go24
# boris.gb 2026-10-13 01:30 - часовой пояс не зашит в образ: задаётся при запуске (TZ из GO24_TZ в compose)
COPY --from=build /out/go24 /usr/local/bin/go24
USER go24
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/go24"]
