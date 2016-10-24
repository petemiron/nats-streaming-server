#!/bin/bash
sudo apt-get update
sudo apt-get install -y golang 
git clone https://github.com/ColinSullivan1/nats-testing.git 
echo 'export GOPATH=/go' >> /home/ubuntu/.profile
sudo GOPATH=/go go get github.com/nats-io/gnatsd
sudo GOPATH=/go go get github.com/nats-io/nats
