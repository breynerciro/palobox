FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /palobox .

FROM alpine:3.22
# vips: miniaturas y fechas de fotos (incluye HEIC). ffmpeg: miniaturas y fechas de videos.
RUN apk add --no-cache vips-tools vips-heif ffmpeg tzdata
COPY --from=build /palobox /usr/local/bin/palobox
# La app de Android que ofrece /app (landing.go). android/build.sh la deja lista con su versión antes de desplegar.
COPY android/app-release-signed.apk /usr/share/palobox/palobox.apk
COPY android/app-version /usr/share/palobox/palobox.apk.version
ENV DATA_DIR=/data TZ=America/Bogota
EXPOSE 8080
# Mismo UID que el dueño de los datos en el servidor: los archivos quedan a su nombre y Samba los ve.
USER 1000:1000
ENTRYPOINT ["palobox"]
