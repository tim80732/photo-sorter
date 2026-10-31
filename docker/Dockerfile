# Built from the Linux binary that build.ps1 puts next to this file.
# The image is just the static binary: no OS, no shell.
FROM scratch
COPY photosorter /photosorter
ENV PHOTOSORTER_LISTEN=:8095 PHOTOSORTER_CONFIG=/config PHOTOSORTER_ROOTS=/camera-photo
EXPOSE 8095
VOLUME ["/config"]
ENTRYPOINT ["/photosorter", "serve"]
