GOLINT=$(HOME)/go/bin/golint
STATIC_BUILD=0
LDFLAGS=-X 'main.Version=0.1' -X 'main.BuildTime=`date -u --rfc-3339=ns`'

all: vet test build

lint: $(GOLINT)
	$(GOLINT) ./...

$(GOLINT):
	GO111MODULE=off go get -u golang.org/x/lint/golint

vet:
	go vet ./...

test:
	go test ./...

build:
ifeq ($(STATIC_BUILD), 1)
	CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags "$(LDFLAGS) -extldflags '-static'" -o main .
else
	go build -ldflags "$(LDFLAGS)" -o main .
endif
