# boris.gb 2026-10-11 13:32 - сборка go24: Go собирается в контейнере, на хосте Go не нужен
FROM golang:1-alpine AS build
WORKDIR /src
# boris.gb 2026-10-11 14:39 - go.sum в репозитории: зависимости качаются по зафиксированным версиям и кэшируются отдельным слоем
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/go24 ./cmd/go24

FROM alpine:3.22
# boris.gb 2026-10-24 23:03 - poppler-utils: pdftotext читает текст PDF для поиска по содержимому на Диске (GO24DEV-224)
# boris.gb 2026-10-25 23:55 - postgresql17-client: pg_dump для «Резервного копирования» (GO24DEV-569), версия — как у сервера postgres:17
RUN apk add --no-cache ca-certificates tzdata poppler-utils postgresql17-client && adduser -D -H -u 10001 go24
# boris.gb 2026-10-13 01:30 - часовой пояс не зашит в образ: задаётся при запуске (TZ из GO24_TZ в compose)
COPY --from=build /out/go24 /usr/local/bin/go24
USER go24
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/go24"]
