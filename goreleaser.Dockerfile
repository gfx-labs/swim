from alpine:latest

RUN apk add --no-cache \
	ca-certificates \
	libcap \
	mailcap

ENV XDG_CONFIG_HOME /config
ENV XDG_DATA_HOME /data

EXPOSE 80
EXPOSE 443
EXPOSE 443/udp
EXPOSE 2019

WORKDIR /srv

copy dist/swim_linux_amd64_v2/swim /usr/local/bin/caddy

RUN cp /usr/local/bin/caddy /usr/local/bin/swim

copy docker/Caddyfile /etc/caddy/Caddyfile
copy docker/archive.tar.gz /data/archive.tar.gz

CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]
