FROM ubuntu:16.04

LABEL maintainer="John Jacquay <john@bioteam.net>"

ENV GOPATH /go
ENV GOINSTALL /usr/local/go
ENV PATH $PATH:$GOINSTALL/bin:$GOPATH/bin
ENV CGO_LDFLAGS_ALLOW .*

ENV MINIO_UPDATE off
ENV MINIO_ACCESS_KEY_FILE=access_key \
    MINIO_SECRET_KEY_FILE=secret_key 

WORKDIR /go/src/github.com/minio/

RUN apt-get update
RUN apt-get install -y wget git build-essential

# Install Go
RUN wget https://dl.google.com/go/go1.12.7.linux-amd64.tar.gz
RUN tar -C /usr/local -xzf go1.12.7.linux-amd64.tar.gz
RUN rm -rf go1.12.7.linux-amd64.tar.gz

# Install iRODS + GoRODS
RUN apt-get install -y lsb-release apt-transport-https libxml2
RUN wget -qO - https://packages.irods.org/irods-signing-key.asc | apt-key add -
RUN echo "deb [arch=amd64] https://packages.irods.org/apt/ $(lsb_release -sc) main" | tee /etc/apt/sources.list.d/renci-irods.list
RUN apt-get update
RUN apt-get install -y irods-externals* irods-runtime irods-dev libssl-dev
RUN go get -u github.com/jjacquay712/GoRODS

# Install Minio plus iRODS gateway patch
ENV GO111MODULE on
RUN git clone https://github.com/minio/minio
WORKDIR "minio"
COPY gateway.go cmd/gateway/
COPY irods cmd/gateway/irods
RUN go install -v -ldflags "$(go run buildscripts/gen-ldflags.go)"
RUN go build -tags kqueue -ldflags "-s -w" -o /usr/bin/healthcheck dockerscripts/healthcheck.go
RUN cp dockerscripts/docker-entrypoint.sh /usr/bin/

EXPOSE 9000

ENTRYPOINT ["/usr/bin/docker-entrypoint.sh"]

VOLUME ["/data"]

HEALTHCHECK --interval=1m CMD healthcheck

CMD ["minio"]