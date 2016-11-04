#!/usr/bin/env bash

# per instance setup of systemd template
NATS_DATA_ROOT=/nats-data/

INSTANCE_ID=1
SVC_ID=$SERVICE_NAME-$INSTANCE_ID
sudo useradd -m -d /tmp/$NATS_DATA_ROOT/$SVC_ID -s /usr/sbin/nologin -c "$SVC_ID service user." \
    -uid $((900+$INSTANCE_ID)) -u 1019 $SVC_ID

# copy everything in ./home/ to /home/service_user
sudo rsync -azvh home/* /$NATS_DATA_ROOT/$SVC_ID
sudo chown $SVC_ID:$SVC_ID -R /$NATS_DATA_ROOT/$SVC_ID

sudo systemctl enable $SERVICE_NAME\@$INSTANCE_ID.service
