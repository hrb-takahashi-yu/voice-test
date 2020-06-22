FROM golang:1.14.2
RUN apt-get update \
    && apt-get install -y vim

WORKDIR /go/src
RUN go get github.com/oxequa/realize

CMD ["realize", "start"]