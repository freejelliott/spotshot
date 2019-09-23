FROM golang:1.13-alpine as builder

RUN apk add --no-cache \
    make \
    # coreutils is needed to run `date` to get build time.
    coreutils \
    ca-certificates

RUN mkdir -p /build/pkg
WORKDIR /build

# Get dependencies first as they will change less, hence are more cacheable.
COPY go.mod go.sum /build/
RUN go mod download

COPY pkg /build/pkg/
COPY main.go Makefile /build/
RUN make build STATIC_BUILD=1

# Compile our fake timezone. It is offset to a minute before the end of the month.
# RUN apk add --no-cache tzdata
# RUN offset=$(\
#         secs=$(\
#             # Set $secs to the difference in seconds between the time at the last minute of the month and now.
#             echo "`date -d "2019-09-30 23:58" +%s` - `date +%s`" | bc -l \
#         ); \
#         # Set $offset to something like 277:43, which would be 277 hours and 43 minutes.
#         printf '%02d:%02d\n' $(($secs/3600)) $(($secs%3600/60)) \
#     ) && \
#     # Compile the timezone.
#     echo "Zone TEST $offset - TEST" | zic -d . - 

# Use a new image from scratch for the application.
# We've built a static binary so we don't need any dependencies.
FROM scratch
# COPY --from=builder /build/TEST /etc/localtime
COPY --from=builder /build/main /app/main
# The root ca-certificates are needed to make SSL requests.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY cfg/ \
    index.html.tmpl \
    /app/

WORKDIR /app
ENTRYPOINT ["/app/main", "-c", "./config.json"]
