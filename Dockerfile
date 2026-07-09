# Многоступенчатая сборка: компилируем статический бинарник и кладём его в
# distroless-образ. mermaid.js вшит в бинарник через go:embed, поэтому финальный
# образ не содержит ничего лишнего.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /dockerfile-parser ./cmd/dockerfile-parser

FROM gcr.io/distroless/static-debian12
COPY --from=build /dockerfile-parser /dockerfile-parser
EXPOSE 8080
ENTRYPOINT ["/dockerfile-parser"]
