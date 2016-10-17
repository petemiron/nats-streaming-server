#!/bin/bash
# update apt
sudo apt-get update
# install required libs
sudo apt-get -y install unzip
# download nats
curl -L https://github.com/nats-io/nats-streaming-server/releases/download/v0.2.0/nats-streaming-server-linux-amd64.zip > ~/nats-streaming-server-linux-amd64.zip
# unzip
unzip nats-streaming-server-linux-amd64.zip
sudo cp nats-streaming-server-linux-amd64/nats-streaming-server /opt/
sudo chmod +x /opt/nats-streaming-server

# setup systemd
sudo cp natsd.service /etc/systemd/system/
sudo chown root:root /etc/systemd/system/natsd.service
sudo chmod 664 /etc/systemd/system/natsd.service
sudo systemctl enable natsd.service
